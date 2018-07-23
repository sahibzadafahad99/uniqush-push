package http_api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"testing"

	"github.com/uniqush/uniqush-push/push"
	"github.com/uniqush/uniqush-push/srv/apns/common"
	apns_mocks "github.com/uniqush/uniqush-push/srv/apns/http_api/mocks"
)

const (
	bundleID        = "com.example.test"
	mockServiceName = "myService"
)

var (
	pushServiceProvider = initPSP()
	devToken            = []byte("test_device_token")
	payload             = []byte(`{"alert":"test_message"}`)
)

func initPSP() *push.PushServiceProvider {
	psm := push.GetPushServiceManager()
	psm.RegisterPushServiceType(&apns_mocks.MockPushServiceType{})
	psp, err := psm.BuildPushServiceProviderFromMap(map[string]string{
		"service":         mockServiceName,
		"pushservicetype": "apns",
		"cert":            "../apns-test/localhost.cert",
		"key":             "../apns-test/localhost.key",
		"addr":            "gateway.push.apple.com:2195",
		"skipverify":      "true",
		"bundleid":        bundleID,
	})
	if err != nil {
		panic(err)
	}
	return psp
}

// TODO: remove unrelated fields
type mockHTTP2Client struct {
	processorFn func(r *http.Request) (*http.Response, *mockResponse, error)
	// Mocks for responses given json encoded request, TODO write expectations.
	// mockResponses map[string]string
	performed []*mockResponse
	mutex     sync.Mutex
}

func (c *mockHTTP2Client) Do(request *http.Request) (*http.Response, error) {
	result, mockResponse, err := c.processorFn(request)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.performed = append(c.performed, mockResponse)
	return result, err
}

var _ HTTPClient = &mockHTTP2Client{}

type mockResponse struct {
	impl    *bytes.Reader
	closed  bool
	request *http.Request
}

func (r *mockResponse) Read(p []byte) (n int, err error) {
	return r.impl.Read(p)
}

func (r *mockResponse) Close() error {
	r.closed = true
	return nil
}

var _ io.ReadCloser = &mockResponse{}

func newMockResponse(contents []byte, request *http.Request) *mockResponse {
	return &mockResponse{
		impl:    bytes.NewReader(contents),
		closed:  false,
		request: request,
	}
}

func mockAPNSRequest(requestProcessor *HTTPPushRequestProcessor, fn func(r *http.Request) (*http.Response, *mockResponse, error)) *mockHTTP2Client {
	mockClient := &mockHTTP2Client{
		processorFn: fn,
	}
	requestProcessor.clientFactory = func(_ *http.Transport) HTTPClient {
		return mockClient
	}
	return mockClient
}

func newPushRequest() (*common.PushRequest, chan push.Error, chan *common.APNSResult) {
	errChan := make(chan push.Error)
	resChan := make(chan *common.APNSResult, 1)
	request := &common.PushRequest{
		PSP:       pushServiceProvider,
		Devtokens: [][]byte{devToken},
		Payload:   payload,
		ErrChan:   errChan,
		ResChan:   resChan,
	}

	return request, errChan, resChan
}

func newHTTPRequestProcessor() *HTTPPushRequestProcessor {
	res := NewRequestProcessor().(*HTTPPushRequestProcessor)
	// Force tests to override this or crash.
	res.clientFactory = nil
	return res
}

func expectHeaderToHaveValue(t *testing.T, r *http.Request, headerName string, expectedHeaderValue string) {
	t.Helper()
	if headerValues := r.Header[headerName]; len(headerValues) > 0 {
		if len(headerValues) > 1 {
			t.Errorf("Too many header values for %s header, expected 1 value, got values: %v", headerName, headerValues)
		}
		headerValue := headerValues[0]
		if headerValue != expectedHeaderValue {
			t.Errorf("Expected header value for %s header to be %s, got %s", headerName, expectedHeaderValue, headerValue)
		}
	} else {
		t.Errorf("Missing %s header", headerName)
	}
}

func handleAPNSResultOrEmitTestError(t *testing.T, resChan <-chan *common.APNSResult, errChan <-chan push.Error, resultHandler func(*common.APNSResult)) {
	select {
	case res := <-resChan:
		resultHandler(res)
	case err := <-errChan:
		if err != nil {
			t.Fatalf("Response was unexpectedly an error: %v\n", err)
			return
		}
		res := <-resChan
		resultHandler(res)
	}
}
func TestAddRequestPushSuccessful(t *testing.T) {
	requestProcessor := newHTTPRequestProcessor()

	request, errChan, resChan := newPushRequest()
	mockClient := mockAPNSRequest(requestProcessor, func(r *http.Request) (*http.Response, *mockResponse, error) {
		if auth, ok := r.Header["Authorization"]; ok {
			// temporarily disabled
			t.Errorf("Unexpected Authorization header %v", auth)
		}
		expectHeaderToHaveValue(t, r, "apns-expiration", "0") // Specific to fork, would need to mock time or TTL otherwise
		expectHeaderToHaveValue(t, r, "apns-priority", "10")
		expectHeaderToHaveValue(t, r, "apns-topic", bundleID)
		requestBody, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Error("Error reading request body:", err)
		}
		if !bytes.Equal(requestBody, payload) {
			t.Errorf("Wrong message payload, expected `%v`, got `%v`", payload, requestBody)
		}
		// Return empty body
		body := newMockResponse([]byte{}, r)
		response := &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
		}
		return response, body, nil
	})

	requestProcessor.AddRequest(request)

	handleAPNSResultOrEmitTestError(t, resChan, errChan, func(res *common.APNSResult) {
		if res.MsgID == 0 {
			t.Fatal("Expected non-zero message id, got zero")
		}
	})

	actualPerformed := len(mockClient.performed)
	if actualPerformed != 1 {
		t.Fatalf("Expected 1 request to be performed, but %d were", actualPerformed)
	}
}

// Test sending 10 pushes at a time, to catch any obvious race conditions in `go test -race`.
func TestAddRequestPushSuccessfulWhenConcurrent(t *testing.T) {
	requestProcessor := newHTTPRequestProcessor()

	iterationCount := 10
	wg := sync.WaitGroup{}
	wg.Add(iterationCount)

	mockClient := mockAPNSRequest(requestProcessor, func(r *http.Request) (*http.Response, *mockResponse, error) {
		if auth, ok := r.Header["Authorization"]; ok {
			// temporarily disabled
			t.Errorf("Unexpected Authorization header %v", auth)
		}
		expectHeaderToHaveValue(t, r, "apns-expiration", "0") // Specific to fork, would need to mock time or TTL otherwise
		expectHeaderToHaveValue(t, r, "apns-priority", "10")
		expectHeaderToHaveValue(t, r, "apns-topic", bundleID)
		requestBody, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Error("Error reading request body:", err)
		}
		if !bytes.Equal(requestBody, payload) {
			t.Errorf("Wrong message payload, expected `%v`, got `%v`", payload, requestBody)
		}
		// Return empty body
		body := newMockResponse([]byte{}, r)
		response := &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
		}
		return response, body, nil
	})
	for i := 0; i < iterationCount; i++ {
		go func() {
			request, errChan, resChan := newPushRequest()

			requestProcessor.AddRequest(request)

			defer wg.Done()

			handleAPNSResultOrEmitTestError(t, resChan, errChan, func(res *common.APNSResult) {
				if res.MsgID == 0 {
					t.Error("Expected non-zero message id, got zero")
				}
			})
		}()
	}
	wg.Wait()
	actualPerformed := len(mockClient.performed)
	if actualPerformed != iterationCount {
		t.Fatalf("Expected %d requests to be performed, but %d were", iterationCount, actualPerformed)
	}
}

func TestAddRequestPushFailConnectionError(t *testing.T) {
	requestProcessor := newHTTPRequestProcessor()

	request, errChan, _ := newPushRequest()
	mockAPNSRequest(requestProcessor, func(r *http.Request) (*http.Response, *mockResponse, error) {
		return nil, nil, fmt.Errorf("No connection")
	})

	requestProcessor.AddRequest(request)

	err := <-errChan
	if _, ok := err.(*push.ConnectionError); !ok {
		t.Fatal("Expected Connection error, got", err)
	}
}

func newMockJSONResponse(r *http.Request, status int, responseData *APNSErrorResponse) (*http.Response, *mockResponse, error) {
	responseBytes, err := json.Marshal(responseData)
	if err != nil {
		panic(fmt.Sprintf("newMockJSONResponse failed: %v", err))
	}
	body := newMockResponse(responseBytes, r)
	response := &http.Response{
		StatusCode: status,
		Body:       body,
	}
	return response, body, nil
}

func TestAddRequestPushFailNotificationError(t *testing.T) {
	requestProcessor := newHTTPRequestProcessor()

	request, errChan, resChan := newPushRequest()
	mockAPNSRequest(requestProcessor, func(r *http.Request) (*http.Response, *mockResponse, error) {
		response := &APNSErrorResponse{
			Reason: "BadDeviceToken",
		}
		return newMockJSONResponse(r, http.StatusBadRequest, response)
	})

	requestProcessor.AddRequest(request)

	handleAPNSResultOrEmitTestError(t, resChan, errChan, func(res *common.APNSResult) {
		if res.Status != common.Status8Unsubscribe {
			t.Fatalf("Expected 8 (unsubscribe), got %d", res.Status)
		}
		if res.MsgID == 0 {
			t.Fatal("Expected non-zero message id, got zero")
		}
	})
}

// TODO: Add test of decoding error response with timestamp

func TestGetMaxPayloadSize(t *testing.T) {
	maxPayloadSize := NewRequestProcessor().GetMaxPayloadSize()
	if maxPayloadSize != 4096 {
		t.Fatalf("Wrong max payload, expected `4096`, got `%d`", maxPayloadSize)
	}
}
