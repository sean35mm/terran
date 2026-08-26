package terran

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var envReferencePattern = regexp.MustCompile(`^\{env:[A-Z][A-Z0-9_]*\}$`)
var windowsAbsolutePathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
var embeddedHTTPURLPattern = regexp.MustCompile("(?i)(?:^|[\\s=(:'\"\\[<{},])(https?://[^\\s\"'<>`]+)")

func validateOpenCodeConfig(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeUniqueJSONValue(dec, "$")
	if err != nil {
		return fmt.Errorf("config must be strict JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("config must be a JSON object")
	}
	if token, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("config has trailing JSON value %v", token)
		}
		return fmt.Errorf("config has trailing JSON: %w", err)
	}
	return validateConfigValue(value, "", "$")
}

func decodeUniqueJSONValue(dec *json.Decoder, path string) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key at %s is not a string", path)
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate key %q at %s", key, path)
			}
			value, err := decodeUniqueJSONValue(dec, path+"."+key)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if end, err := dec.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("unterminated object at %s", path)
		}
		return object, nil
	case '[':
		var array []any
		for index := 0; dec.More(); index++ {
			value, err := decodeUniqueJSONValue(dec, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("unterminated array at %s", path)
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q at %s", delim, path)
	}
}

func validateConfigValue(value any, key, path string) error {
	if credentialKey(key) {
		reference, ok := value.(string)
		if !ok || !envReferencePattern.MatchString(reference) {
			return fmt.Errorf("credential-bearing value at %s must use an explicit {env:VAR} reference", path)
		}
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if err := validateConfigValue(child, childKey, path+"."+childKey); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := validateConfigValue(child, key, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case string:
		if containsAbsoluteMachinePath(typed) {
			return fmt.Errorf("absolute machine path is not allowed at %s", path)
		}
		if err := validateStringURLs(typed); err != nil {
			return fmt.Errorf("invalid URL at %s: %w", path, err)
		}
	}
	return nil
}

func credentialKey(key string) bool {
	compact := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, key)
	if compact == "key" || compact == "credentials" || strings.HasSuffix(compact, "authorization") || strings.HasSuffix(compact, "password") || strings.HasSuffix(compact, "passphrase") || strings.HasSuffix(compact, "credential") || strings.HasSuffix(compact, "secret") || strings.HasSuffix(compact, "token") || strings.HasSuffix(compact, "apikey") || strings.HasSuffix(compact, "accesskey") || strings.HasSuffix(compact, "accesskeyid") {
		return true
	}
	if strings.Contains(compact, "privatekey") || strings.Contains(compact, "clientkey") || strings.Contains(compact, "clientsecret") || strings.Contains(compact, "secretaccesskey") || strings.Contains(compact, "signingkey") {
		return true
	}
	words := normalizedKeyWords(key)
	if len(words) == 0 {
		return false
	}
	switch words[len(words)-1] {
	case "cookie", "auth", "bearer", "pat":
		return true
	default:
		return false
	}
}

func normalizedKeyWords(key string) []string {
	var words []string
	var word []rune
	var previousLower bool
	flush := func() {
		if len(word) != 0 {
			words = append(words, string(word))
			word = nil
		}
	}
	for _, r := range key {
		upper := r >= 'A' && r <= 'Z'
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !upper && !lower && !digit {
			flush()
			previousLower = false
			continue
		}
		if upper && previousLower {
			flush()
		}
		if upper {
			r += 'a' - 'A'
		}
		word = append(word, r)
		previousLower = lower || digit
	}
	flush()
	return words
}

func localOnlyURL(value string) (bool, error) {
	lowerValue := strings.ToLower(value)
	if strings.HasPrefix(lowerValue, "file:") {
		return false, fmt.Errorf("file URLs are not allowed")
	}
	if !strings.HasPrefix(lowerValue, "http://") && !strings.HasPrefix(lowerValue, "https://") {
		return false, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return false, fmt.Errorf("malformed HTTP URL")
	}
	if parsed.User != nil {
		return false, fmt.Errorf("URL userinfo is not allowed")
	}
	for queryKey := range parsed.Query() {
		if credentialKey(queryKey) {
			return false, fmt.Errorf("credential-bearing URL query parameter %q is not allowed", queryKey)
		}
	}
	host := strings.TrimRight(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true, nil
	}
	if address := net.ParseIP(host); address != nil {
		return address.IsLoopback() || address.IsPrivate() || address.IsUnspecified() || address.IsLinkLocalUnicast(), nil
	}
	return false, nil
}

func validateStringURLs(value string) error {
	for _, candidate := range stringCandidates(value) {
		if strings.HasPrefix(strings.ToLower(candidate), "file:") {
			return fmt.Errorf("file URLs are not allowed")
		}
	}
	for _, match := range embeddedHTTPURLPattern.FindAllStringSubmatch(value, -1) {
		candidate := match[1]
		candidate = strings.TrimRight(candidate, ".,;)}")
		local, err := localOnlyURL(candidate)
		if err != nil {
			return err
		}
		if local {
			return fmt.Errorf("local-only URL is not allowed")
		}
	}
	return nil
}

func containsAbsoluteMachinePath(value string) bool {
	for _, candidate := range stringCandidates(value) {
		if filepath.IsAbs(candidate) || windowsAbsolutePathPattern.MatchString(candidate) || strings.HasPrefix(candidate, `\\`) || strings.HasPrefix(candidate, "//") {
			if candidate != "/" {
				return true
			}
		}
	}
	return false
}

func stringCandidates(value string) []string {
	var candidates []string
	for _, field := range strings.Fields(value) {
		for _, candidate := range strings.Split(field, "=") {
			candidate = strings.Trim(candidate, `"'()[]{}<>,;`)
			if candidate != "" {
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates
}
