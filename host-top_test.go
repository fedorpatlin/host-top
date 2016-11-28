package main

import "testing"

func TestIsRequest(t *testing.T) {
	req := isRequest([]byte("GET /some/uri/here HTTP/1.1"))
	if !req {
		t.Fail()
	}
	req = isRequest([]byte("GET /some/shit HTTP/1.1\r\nHost: localhost  \r\nSome othe string"))
	if !req {
		t.Fail()
	}
	req = isRequest([]byte("Accept: application/json"))
	if req {
		t.Fail()
	}
	req = isRequest([]byte("Somestring"))
	if req {
		t.Fail()
	}

}

func TestExtractHost(t *testing.T) {
	goodText := "GET /some/shit HTTP/1.1\r\n Host: localhost  \r\nSome othe string"
	badText := "GET /some/shit HTTP/1.1\r\n Accept: application/json  \r\nSome othe string"
	if host, err := hostExtractor([]byte(goodText)); err == nil {
		if host != "localhost" {
			t.Fail()
		}
	} else {
		t.Fail()
	}

	if _, err := hostExtractor([]byte(badText)); err == nil {
		t.Fail()
	}
}
