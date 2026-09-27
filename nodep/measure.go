package nodep

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"time"
)

const (
	PingDelayTimeout int64 = 11000
	PingDelayError   int64 = 10000
)

// get the delay of some outbound.
// timeout means how long the http request will be cancelled if no response, in units of seconds.
// url means the website we use to test speed. "https://www.google.com" is a good choice for most cases.
// proxy means the local http/socks5 proxy, like "socks5://[::1]:1080". If proxy is empty, it means no proxy.
func MeasureDelay(timeout int, url string, proxy string) (int64, error) {
	httpTimeout := time.Second * time.Duration(timeout)
	c, err := CoreHTTPClient(httpTimeout, proxy)
	if err != nil {
		return PingDelayError, err
	}
	delay, err := PingHTTPRequest(c, url, timeout)
	if err != nil {
		return delay, err
	}

	return delay, nil
}

func CoreHTTPClient(timeout time.Duration, proxy string) (*http.Client, error) {
	tr := &http.Transport{
		DisableKeepAlives: true,
	}

	if len(proxy) > 0 {
		tr.Proxy = func(r *http.Request) (*url.URL, error) {
			return url.Parse(proxy)
		}
	}

	c := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}

	return c, nil
}

func PingHTTPRequest(c *http.Client, url string, timeout int) (int64, error) {
	start := time.Now()
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return PingDelayError, err
	}
	response, err := c.Do(req)
	delay := time.Since(start).Milliseconds()
	if err != nil {
		precision := delay - int64(timeout)*1000
		if math.Abs(float64(precision)) < 50 {
			return PingDelayTimeout, err
		}
		return PingDelayError, err
	}
	response.Body.Close()
	return delay, nil
}

// PingHTTPRequestWarm measures the request round trip without connection
// setup. The first request opens the connection and alone decides
// reachability and timeout; the second reuses it within the remaining
// timeout. It reports the smaller delay, falling back to the first when the
// second fails, so a reachable server is never reported as failed.
// The client must keep connections alive for the second request to reuse one.
func PingHTTPRequestWarm(c *http.Client, url string, timeout int) (int64, error) {
	deadline := time.Now().Add(time.Second * time.Duration(timeout))
	first, err := PingHTTPRequest(c, url, timeout)
	if err != nil {
		return first, err
	}

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return first, nil
	}
	start := time.Now()
	response, err := c.Do(req)
	if err != nil {
		return first, nil
	}
	second := time.Since(start).Milliseconds()
	response.Body.Close()
	return min(first, second), nil
}
