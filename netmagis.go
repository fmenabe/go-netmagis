package netmagis

import (
	"fmt"
	//"io/ioutil"
	"golang.org/x/net/html"
	"net"
	"net/url"
	//"github.com/lucasuyezu/golang-cas-client"
	//"net/http"
	//"net/http/cookiejar"
	"regexp"
	"strings"
	//"time"
	"github.com/antchfx/htmlquery"
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
 * Client
 */
type NetmagisClient struct {
	BaseUrl string
	//Username   string
	//Password   string
	HttpClient *HttpClient
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
			fmt.Sprintf(
				"unable to retrieve CAS URL: %s",
				err.Error(),
			),
		}
	}
	casLoginUrl := res.Header["Location"][0]

	// Connect to Netmagis through CAS
	cas := CasClient{LoginUrl: casLoginUrl, HttpClient: httpClient}
	err = cas.Connect(username, password)
	if err != nil {
		return nil, err
	}

	// Return client
	client := &NetmagisClient{
		BaseUrl:    url,
		HttpClient: httpClient,
	}
	return client, nil
}

// `host` can be a FQDN or an IP.
func checkFqdn(host string) bool {
	return fqdnRegexp.Match([]byte(host))
}

func checkIp(host string) bool {
	return net.ParseIP(host) != nil
}

func getNodeText(node *html.Node) string {
	return strings.TrimSpace(htmlquery.InnerText(node))
}

func getMapValue(mapInstance map[string]interface{}, key string, defaultValue interface{}) interface{} {
	value, found := mapInstance[key]
	if found {
		return value
	}
	return defaultValue
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
		return nil, &NetmagisError{"host is not a FQDN or and IP address"}
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
		return nil, &NetmagisError{"unable to parse HTML"}
	}
	nodes := htmlquery.Find(doc, "//td[@class='tab-text10']")
	hostParams := map[string]interface{}{
		"name": getNodeText(nodes[1]),
		"ip":   getNodeText(nodes[3]),
		"mac":  getNodeText(nodes[5]),
		"dhcp": func() string {
			profile := getNodeText(nodes[7])
			if profile == "No profile" {
				return ""
			} else {
				return profile
			}
		}(),
		"type": getNodeText(nodes[9]),
		"smtp": func() bool {
			if getNodeText(nodes[11]) == "Yes" {
				return true
			} else {
				return false
			}
		}(),
		"comment": getNodeText(nodes[13]),
		"owner": map[string]string{
			"name": getNodeText(nodes[15]),
			"mail": getNodeText(nodes[17]),
		},
	}

	if len(nodes) == 20 {
		hostParams["aliases"] = []string{}
		hostParams["groups"] = strings.Split(getNodeText(nodes[19]), " ")
	} else if len(nodes) == 22 {
		hostParams["aliases"] = strings.Split(getNodeText(nodes[19]), " ")
		hostParams["groups"] = strings.Split(getNodeText(nodes[21]), " ")
	} else {
		return nil, &NetmagisError{"unexpected number of fields"}
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
