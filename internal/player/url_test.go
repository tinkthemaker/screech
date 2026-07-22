package player

import "testing"

func TestValidateStreamURL(t *testing.T) {
	ok := []string{
		"http://ice1.somafm.com/groovesalad-128-mp3",
		"https://stream.radioparadise.com/aac-128",
		"HTTPS://Example.com/stream",
	}
	for _, u := range ok {
		if err := ValidateStreamURL(u); err != nil {
			t.Errorf("ValidateStreamURL(%q) = %v, want nil", u, err)
		}
	}

	bad := []string{
		"",
		"   ",
		"file:///etc/passwd",
		"file://C:/Windows/System32/config/SAM",
		"/etc/passwd",
		"data:text/plain;base64,AAAA",
		"ftp://example.com/x",
		"javascript:alert(1)",
		"http://",
		"https:///no-host",
	}
	for _, u := range bad {
		if err := ValidateStreamURL(u); err == nil {
			t.Errorf("ValidateStreamURL(%q) = nil, want error", u)
		}
	}
}
