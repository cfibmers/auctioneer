package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

var ErrBadRead = errors.New("bad read!")

func TestHandlers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Handlers Suite")
}

func newTestRequest(body interface{}) *http.Request {
	var reader io.Reader
	switch body := body.(type) {
	case string:
		reader = strings.NewReader(body)
	case []byte:
		reader = bytes.NewReader(body)
	default:
		jsonBytes, err := json.Marshal(body)
		Ω(err).ShouldNot(HaveOccurred())
		reader = bytes.NewReader(jsonBytes)
	}

	request, err := http.NewRequest("", "", reader)
	Ω(err).ToNot(HaveOccurred())
	return request
}

type badReader struct{}

func (_ badReader) Read(_ []byte) (int, error) {
	return 0, ErrBadRead
}

func (_ badReader) Close() error {
	return nil
}