package netmagis

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

type HttpClient struct {
	HttpClient *http.Client
}

//
// Initialize HTTP client
//
// Redirects are not automatically followed so headers can be parsed for retrieving
// URLs (like CAS URL).
//
// Use cookiejars for keeping HTTP cookies through requests.
//
// FIXME: Manage parameters (like timeout, other?) with random value. Looks like in Go
// there is no default value for function parameters, so we can use a
// map[string]interface{} map or even a strucutre.
//
//func NewHttpClient(url string, username string, password string, params map[string]interface{}) (*Client, error) {
func NewHttpClient() (*HttpClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf(
				"unable to initialize cookiejar: %s", err.Error(),
			),
		}
	}

	/*
		//timeout = int(params["timeout"])
		timeout := 60
		if x, found := params["timeout"]; found {
			if conv, ok := x.(int); !ok {
				fmt.Println("invalid type for timeout!")
			} else {
				timeout = conv
			}
		}
	*/

	httpClient := &HttpClient{
		HttpClient: &http.Client{
			Timeout: time.Duration(60) * time.Second,
			// Disable redirects
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Jar: jar,
		},
	}
	return httpClient, nil
}

func (c *HttpClient) Get(url string) (*http.Response, error) {
	res, err := c.HttpClient.Get(url)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf(
				"HTTP error: %s", err.Error(),
			),
		}
	}
	return res, nil
}

func (c *HttpClient) GetRedirect(url string) (*http.Response, error) {
	res, err := c.HttpClient.Get(url)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != 301 && res.StatusCode != 302 {
		return nil, &NetmagisError{
			fmt.Sprintf(
				"invalid status code: '%d' (30{1,2} expected)", res.StatusCode,
			),
		}
	}

	return res, nil
}

func (c *HttpClient) ReadBody(res *http.Response) ([]byte, error) {
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("body read error: %s", err.Error()),
		}
	}
	return body, nil
}

func (c *HttpClient) PostForm(url string, formData url.Values) (*http.Response, error) {
	res, err := c.HttpClient.PostForm(url, formData)
	if err != nil {
		return nil, &NetmagisError{
			fmt.Sprintf("HTTP error: %s", err.Error()),
		}
	}

	return res, nil
}
