package netmagis

import (
	"fmt"
	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	fqdnRegexp           = regexp.MustCompile(`^[0-9a-zA-Z-]{2,63}(\.[a-zA-Z-]{2,63})+\.[a-zA-Z]{2,63}$`)
	errorRegexp          = regexp.MustCompile(`<blockquote><FONT COLOR="#FF0000">(.*)</FONT></blockquote>`)
	hostNotFoundRegexp   = regexp.MustCompile(`String '[^']*' not found`)
	searchRegexpValidate = regexp.MustCompile(`is a.* in view `)
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

func splitFqdn(fqdn string) (string, string) {
	res := strings.SplitN(fqdn, ".", 2)
	return res[0], res[1]
}

func nodeText(node *html.Node) string {
	return strings.TrimSpace(htmlquery.InnerText(node))
}

func convertInt(value interface{}) string {
	if v, ok := value.(int); ok {
		return strconv.Itoa(v)
	}
	return value.(string)
}

func convertBool(value interface{}) string {
	if v, ok := value.(bool); ok {
		if v {
			return "1"
		}
		return "0"
	}
	return value.(string)
}

/*
 * Client
 */
type NetmagisClient struct {
	BaseUrl    string
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

func (c *NetmagisClient) Call(uri string, formData url.Values, validateFunc func(body string) bool) (string, error) {
	res, err := c.HttpClient.PostForm(c.JoinUrl(uri), formData)
	if err != nil {
		return "", &NetmagisError{fmt.Sprintf("ClientError: %s", err.Error())}
		//return &NetmagisError{fmt.Sprintf("%s: HTTP request error: %s", name, err.Error())}
	}
	body, _ := c.HttpClient.ReadBody(res)
	bodyString := string(body)

	if strings.Contains(bodyString, "<h2>Error!</h2>") {
		errorMsg := strings.Trim(string(errorRegexp.FindSubmatch(body)[1]), `"`)
		return "", &NetmagisError{fmt.Sprintf("NetmagisError: %s", errorMsg)}
	}

	if !validateFunc(bodyString) {
		return "", &NetmagisError{
			fmt.Sprintf(
				"ValidationError: unexpected output (raw HTML answer for debug): %s", body,
			),
		}
	}
	return bodyString, nil
}

func (c *NetmagisClient) Search(host string) (map[string]interface{}, error) {
	// Check input host
	if !checkIp(host) && !checkFqdn(host) {
		return nil, &NetmagisError{
			fmt.Sprintf("host '%s' is not a FQDN or and IP address", host),
		}
	}

	// Search host and parse HTML response

	checkFunc := func(body string) bool {
		return searchRegexpValidate.MatchString(body) || hostNotFoundRegexp.MatchString(body)
	}
	body, err := c.Call("/search", url.Values{"q": {host}}, checkFunc)
	if err != nil {
		return nil, err
	}
	if hostNotFoundRegexp.MatchString(body) {
		return nil, nil
	}

	doc, err := htmlquery.Parse(strings.NewReader(body))
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("unable to parse /search HTML response: %s", err.Error()),
		}
	}

	// Parse all <td> in the page to generating output. The HTML table contains two
	// columns: field and value. The returned keys are mapped to be coherent with
	// other API calls (but some fields like aliases and groups are not used by
	// other calls).
	hostParams := map[string]interface{}{}
	nodes := htmlquery.Find(doc, "//td[@class='tab-text10']")
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

			switch field {
			case "smtp_emit_right":
				hostParams[field] = map[string]bool{"Yes": true, "No": false, "": false}[value]
			case "dhcp_profile":
				profile := ""
				if value != "No profile" {
					profile = value
				}
				hostParams[field] = profile
			case "ttl":
				hostParams[field] = func() int { v, _ := strconv.Atoi(value); return v }()
			case "aliases", "allowed_groups":
				hostParams[field] = strings.Split(value, " ")
				hostParams[field] = strings.Split(value, " ")
			default:
				hostParams[field] = value
			}

			field = ""
		}
	}
	// Computed field indicating if the entry is an alias
	hostParams["is_alias"] = host != hostParams["name"]

	return hostParams, nil
}

// Parse /mod form to retrieve informations about a host.
func (c *NetmagisClient) GetHost(fqdn string) (map[string]interface{}, error) {
	name, domain := splitFqdn(fqdn)

	// Get host modification form
	body, err := c.Call(
		"/mod",
		url.Values{
			"action": {"edit"},
			"name":   {name},
			"domain": {domain},
		},
		func(body string) bool { return true },
	)
	if err != nil {
		// Bypass the error returned by Netmagis for returning nil when the host
		// does not exists.
		hostNotFoundRegexp := regexp.MustCompile(`Name '[^']*' does not exist`)
		if hostNotFoundRegexp.MatchString(err.Error()) {
			return nil, nil
		}
		return nil, err
	}

	// Parse HTML response
	doc, err := htmlquery.Parse(strings.NewReader(body))
	if err != nil {
		errMsg := fmt.Sprintf("unable to parse /mod HTML response: %s", err.Error())
		return nil, &NetmagisError{errMsg}
	}

	// Parse form inputs
	hostParams := map[string]interface{}{}
	for _, node := range htmlquery.Find(doc, "//input") {
		inputName := htmlquery.SelectAttr(node, "name")
		inputValue := htmlquery.SelectAttr(node, "value")
		ignoreFields := map[string]bool{
			"":        true,
			"action":  true,
			"confirm": true,
		}
		if !ignoreFields[inputName] {
			if inputName == "sendsmtp" {
				if len(node.Attr) == 4 && node.Attr[3].Key == "checked" {
					inputValue = "1"
				} else {
					inputValue = "0"
				}
			}
			hostParams[inputName] = string(inputValue)
		}
	}

	// Parse form selects
	for _, node := range htmlquery.Find(doc, "//select") {
		selectName := htmlquery.SelectAttr(node, "name")
		found := false
		// Parse options
		for _, o := range htmlquery.Find(node, "//option") {
			value := htmlquery.SelectAttr(o, "value")
			// Check if the selected attr is set
			if len(o.Attr) == 2 && o.Attr[1].Key == "selected" {
				hostParams[selectName] = value
				found = true
				break
			}
		}

		if selectName == "iddhcpprof" && !found {
			hostParams[selectName] = "0"
		}
	}

	return hostParams, nil
}

func (c *NetmagisClient) AddHost(fqdn string, ip string, params map[string]interface{}) error {
	name, domain := splitFqdn(fqdn)

	// Check if host already exists
	host, err := c.GetHost(fqdn)
	if err != nil {
		return &NetmagisError{fmt.Sprintf("unable to retrieve host: %s", err.Error())}
	}
	if host != nil {
		if !try(params, "multiple", false).(bool) {
			return &NetmagisError{
				fmt.Sprintf(
					"host '%s' already declared, use `multiple` parameter to allow round-robin DNS",
					fqdn,
				),
			}
		}
	}

	// Format and send request
	formData := url.Values{
		"action":     {"add-host"},
		"idview":     {"1"},
		"addr":       {ip},
		"name":       {name},
		"domain":     {domain},
		"naddr":      {"1"},
		"confirm":    {"yes"},
		"ttl":        {convertInt(try(params, "ttl", ""))},
		"mac":        {try(params, "mac", "").(string)},
		"iddhcpprof": {convertInt(try(params, "iddhcpprof", 0))},
		"hinfo":      {try(params, "hinfo", "PC/Unix").(string)},
		"comment":    {try(params, "comment", "").(string)},
		"respname":   {try(params, "respname", "").(string)},
		"respmail":   {try(params, "respmail", "").(string)},
		"sendsmtp":   {convertBool(try(params, "sendsmtp", false))},
	}
	if formData["sendsmtp"][0] == "0" {
		delete(formData, "sendsmtp")
	}

	checkFunc := func(body string) bool {
		return strings.Contains(body, "Host has been added.")
	}

	if _, err := c.Call("/add", formData, checkFunc); err != nil {
		return err
	}
	return nil
}

func (c *NetmagisClient) UpdateHost(fqdn string, idrr string, params map[string]interface{}) error {
	name, domain := splitFqdn(fqdn)

	formData := url.Values{
		"action":     {"store"},
		"confirm":    {"yes"},
		"idrr":       {idrr},
		"idview":     {"1"},
		"name":       {name},
		"domain":     {domain},
		"ttl":        {convertInt(try(params, "ttl", ""))},
		"mac":        {try(params, "mac", "").(string)},
		"iddhcpprof": {convertInt(try(params, "iddhcpprof", 0))},
		"hinfo":      {try(params, "hinfo", "PC/Unix").(string)},
		"comment":    {try(params, "comment", "").(string)},
		"respname":   {try(params, "respname", "").(string)},
		"respmail":   {try(params, "respmail", "").(string)},
		"sendsmtp":   {convertBool(try(params, "sendsmtp", false))},
	}
	if formData["sendsmtp"][0] == "0" {
		delete(formData, "sendsmtp")
	}

	checkFunc := func(body string) bool {
		return strings.Contains(body, "The modification has been stored in database")
	}

	if _, err := c.Call("/mod", formData, checkFunc); err != nil {
		return err
	}
	return nil
}

func (c *NetmagisClient) DelHost(fqdn string) error {
	name, domain := splitFqdn(fqdn)
	formData := url.Values{
		"idviews": {"1"},
		"name":    {name},
		"domain":  {domain},
	}
	checkFunc := func(body string) bool {
		return strings.Contains(body, "has been removed")
	}

	if _, err := c.Call("/del", formData, checkFunc); err != nil {
		return err
	}
	return nil
}

func (c *NetmagisClient) AddAlias(cname string, data string) error {
	cnameName, cnameDomain := splitFqdn(cname)
	dataName, dataDomain := splitFqdn(data)

	formData := url.Values{
		"action":    {"add-alias"},
		"name":      {cnameName},
		"domain":    {cnameDomain},
		"nameref":   {dataName},
		"domainref": {dataDomain},
		"idview":    {"1"},
	}
	checkFunc := func(body string) bool {
		return strings.Contains(body, "The alias has been added")
	}

	if _, err := c.Call("/del", formData, checkFunc); err != nil {
		return err
	}
	return nil
}
