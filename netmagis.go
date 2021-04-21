package netmagis

import (
	"fmt"
	"golang.org/x/net/html"
	"io/ioutil"
	"net"
	"net/url"
	//"github.com/lucasuyezu/golang-cas-client"
	//"net/http"
	//"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	//"time"
	"github.com/antchfx/htmlquery"
	"gopkg.in/yaml.v2"
)

var (
	fqdnRegexp         = regexp.MustCompile(`^[0-9a-zA-Z-]{2,63}(\.[a-zA-Z-]{2,63})+\.[a-zA-Z]{2,63}$`)
	hostNotFoundRegexp = regexp.MustCompile(`String '[^']*' not found`)

/*
	tdRegexp           = regexp.MustCompile(
		//		`<td .*class="tab-text10">([^<]*)</td>`,
		`<td .*class="tab-text10">(?!(</td>)*)</td>`,
	)
*/
)
 /*
 * Utils
 */
func try(mapInstance map[string]interface{}, key string, defaultValue interface{}) interface{} {
	value, found := mapInstance[key]
	if found {
		return value
	}
	return defaultValue
}

// `host` can be a FQDN or an IP.
func checkFqdn(host string) bool {
	return fqdnRegexp.Match([]byte(host))
}

func checkIp(host string) bool {
	return net.ParseIP(host) != nil
}

func nodeText(node *html.Node) string {
	return strings.TrimSpace(htmlquery.InnerText(node))
}

func splitFqdn(fqdn string) (string, string) {
	res := strings.SplitN(fqdn, ".", 2)
	return res[0], res[1]
}

/*
 * Client
 */
type NetmagisClient struct {
	BaseUrl string
	//Username   string
	//Password   string
	HttpClient *HttpClient
}

type YamlConfig struct {
	Netmagis struct {
		Url      string `yaml:"url"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	}
}

func FromConfig(filepath string) (*NetmagisClient, error) {
	config := YamlConfig{}

	fileContent, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("FromConfig: unable to load YAML file: %s", err.Error()),
		}
	}

	err = yaml.Unmarshal(fileContent, &config)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("FromConfig: unable to parse YAML content: %s", err.Error()),
		}
	}

	if config.Netmagis.Url == "" {
		return nil, &NetmagisError{"FromConfig: URL not defined"}
	}
	if config.Netmagis.Username == "" {
		return nil, &NetmagisError{"FromConfig: username not defined"}
	}
	if config.Netmagis.Password == "" {
		return nil, &NetmagisError{"FromConfig: password not defined"}
	}

	return NewClient(
		config.Netmagis.Url, config.Netmagis.Username, config.Netmagis.Password,
	)
}

//
// Authenticate through CAS and return initialized Client struct
//
// FIXME: implement retries on CAS auth (there was random connection problems in some
// Python scripts that were solved by implementing retries).
//
func NewClient(url string, username string, password string) (*NetmagisClient, error) {
	httpClient, err := NewHttpClient()
	if err != nil {
		return nil, err
	}

	// Get CAS URL
	res, err := httpClient.GetRedirect(fmt.Sprintf("%s/start", url))
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("NewClient: unable to retrieve CAS URL: %s", err.Error()),
		}
	}
	casLoginUrl := res.Header["Location"][0]

	// Connect to Netmagis through CAS
	cas := CasClient{LoginUrl: casLoginUrl, HttpClient: httpClient}
	err = cas.Connect(username, password)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("NewClient: CAS error: %s", err.Error()),
		}
	}

	// Return client
	client := &NetmagisClient{
		BaseUrl:    url,
		HttpClient: httpClient,
	}
	return client, nil
}

func (c *NetmagisClient) JoinUrl(paths ...string) string {
	url := c.BaseUrl
	for _, path := range paths {
		url += fmt.Sprintf("/%s", strings.Trim(path, "/"))
	}
	return url
}

func (c *NetmagisClient) GetHost(host string) (map[string]interface{}, error) {
	if !checkIp(host) && !checkFqdn(host) {
		return nil, &NetmagisError{"GetHost: host is not a FQDN or and IP address"}
	}

	formData := url.Values{"q": {host}}
	res, err := c.HttpClient.PostForm(c.JoinUrl("/search"), formData)
	if err != nil {
		return nil, err
	}

	body, err := c.HttpClient.ReadBody(res)
	if err != nil {
		return nil, err
	}

	if hostNotFoundRegexp.Match(body) {
		return nil, nil
	}

	doc, err := htmlquery.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("GetHost: unable to parse HTML response: %s", err.Error()),
		}
	}
	nodes := htmlquery.Find(doc, "//td[@class='tab-text10']")

	hostParams := map[string]interface{}{}
	field := ""
	for idx, node := range nodes {
		if idx%2 == 0 {
			field = nodeText(node)
			field = strings.ReplaceAll(field, "(", "")
			field = strings.ReplaceAll(field, ")", "")
			field = strings.ReplaceAll(field, " ", "_")
			field = strings.ToLower(field)
		} else {
			value := nodeText(node)

			if field == "smtp_emit_right" {
				hostParams[field] = map[string]bool{"Yes": true, "No": false, "": false}[value]
			} else if field == "dhcp_profile" {
				profile := ""
				if value != "No profile" {
					profile = value
				}
				hostParams[field] = profile
			} else if field == "ttl" {
				hostParams[field] = func() int { v, _ := strconv.Atoi(value); return v }()
			} else if field == "aliases" {
				hostParams[field] = strings.Split(value, ",")
			} else if field == "allowed_groups" {
				hostParams[field] = strings.Split(value, ",")
			} else {
				hostParams[field] = value
			}

			field = ""
		}
	}

	if host != hostParams["name"] {
		hostParams["is_alias"] = true
	} else {
		hostParams["is_alias"] = false
	}

	return hostParams, nil
}

func (c *NetmagisClient) AddHost(fqdn string, ip string, params map[string]interface{}) error {
	name, domain := func() (string, string) {
		res := strings.SplitN(fqdn, ".", 2)
		return res[0], res[1]
	}()
	fmt.Println(name, domain)

	formData := url.Values{
		"action":     {"add-host"},
		"idview":     {"1"},
		"addr":       {ip},
		"name":       {name},
		"domain":     {domain},
		"naddr":      {"1"},
		"confirm":    {"no"},
		"ttl":        {string(getMapValue(params, "ttl", 60).(int))},
		"mac":        {getMapValue(params, "mac", "").(string)},
		"iddhcpprof": {string(getMapValue(params, "dhcp", 0).(int))},
		"hinfo":      {getMapValue(params, "type", "PC/Unix").(string)},
		"comment":    {getMapValue(params, "comment", "").(string)},
		"respname":   {getMapValue(params, "owner_name", "").(string)},
		"respmail":   {getMapValue(params, "owner_mail", "").(string)},
		"sendsmtp": {func() string {
			if getMapValue(params, "smtp", false).(bool) {
				return "1"
			}
			return "0"
		}()},
	}
	res, err := c.HttpClient.PostForm(c.JoinUrl("/add"), formData)
	if err != nil {
		return err
	}
	body, _ := c.HttpClient.ReadBody(res)
	fmt.Println(string(body))

	/*
		res, err := c.HttpClient.Get(fmt.Sprintf("%s/admindex", c.BaseURL))
		if err != nil {
			return err
		}
		//body, _ := c.HttpClient.ReadBody(res)
	*/
	return nil
}

func (c *NetmagisClient) AddAlias(alias string, host string, view string) error {
	return nil
}

func (c *NetmagisClient) DelHost(alias string, view string) error {
	return nil
}
