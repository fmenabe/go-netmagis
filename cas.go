package netmagis

import (
	"fmt"
	"net/url"
	"regexp"
)

var (
	executionRegexp = regexp.MustCompile(
		`<input type="hidden" name="execution" value="?([^"]*)"/>`,
	)
	loginErrorRegexp = regexp.MustCompile(
		`<span>Authentication attempt has failed, likely due to invalid ',
		'credentials. Please verify and try again. </span>`,
	)
)

type CasClient struct {
	LoginUrl   string
	HttpClient *HttpClient
}

//
// Connect to CAS
//
func (c *CasClient) Connect(username string, password string) error {
	loginPage, err := c.GetLoginPage()
	if err != nil {
		return &NetmagisError{
			fmt.Sprintf(
				"CAS login page error: %s", err.Error(),
			),
		}
	}

	executionToken, err := c.FindExecutionToken(loginPage)
	if err != nil {
		return &NetmagisError{
			fmt.Sprintf(
				"CAS execution token error: %s", err.Error(),
			),
		}
	}

	err = c.Login(username, password, string(executionToken))
	if err != nil {
		return &NetmagisError{
			fmt.Sprintf(
				"CAS login error: %s", err.Error(),
			),
		}
	}

	return nil
}

func (c *CasClient) GetLoginPage() ([]byte, error) {
	res, err := c.HttpClient.Get(c.LoginUrl)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, &NetmagisError{
			fmt.Sprintf("HTTP Error: %s", res.Status),
		}
	}

	body, err := c.HttpClient.ReadBody(res)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func (c *CasClient) FindExecutionToken(loginPage []byte) ([]byte, error) {
	submatch := executionRegexp.FindSubmatch(loginPage)
	if len(submatch) == 0 {
		return nil, &NetmagisError{"token not found"}
	}
	return submatch[1], nil
}

func (c *CasClient) Login(username string, password string, executionToken string) error {
	formData := url.Values{
		"_eventId":  {"submit"},
		"username":  {username},
		"password":  {password},
		"execution": {executionToken},
	}

	res, err := c.HttpClient.PostForm(c.LoginUrl, formData)
	defer res.Body.Close()
	if err != nil {
		return err
	}

	body, _ := c.HttpClient.ReadBody(res)
	if loginErrorRegexp.Match(body) {
		return &NetmagisError{
			fmt.Sprintf(
				"invalid login or password",
			),
		}
	}

	location := res.Header["Location"][0]
	res, err = c.HttpClient.Get(location)
	defer res.Body.Close()
	if err != nil {
		return &NetmagisError{
			fmt.Sprintf(
				"login call back error: %s", err.Error(),
			),
		}
	}

	return nil
}
