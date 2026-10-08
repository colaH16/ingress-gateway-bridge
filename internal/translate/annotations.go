// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	strictjson "sigs.k8s.io/json"
)

const (
	HTTPSRedirectAnnotation   = AnnotationPrefix + "https-redirect"
	RedirectStatusAnnotation  = AnnotationPrefix + "https-redirect-status-code"
	RequestHeadersAnnotation  = AnnotationPrefix + "request-headers"
	ResponseHeadersAnnotation = AnnotationPrefix + "response-headers"
)

type routeOptions struct {
	redirect bool
	status   int
	filters  []gatewayv1.HTTPRouteFilter
}

type headerInput struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}

type headerFilterInput struct {
	Set    []headerInput `json:"set,omitempty"`
	Add    []headerInput `json:"add,omitempty"`
	Remove []string      `json:"remove,omitempty"`
}

// Foreign annotations are deliberately ignored. Errors never include input values.
func parseAnnotations(annotations map[string]string) (routeOptions, error) {
	options := routeOptions{status: 302}
	var unknown []string
	for name := range annotations {
		if strings.HasPrefix(name, AnnotationPrefix) && name != HTTPSRedirectAnnotation &&
			name != RedirectStatusAnnotation && name != RequestHeadersAnnotation && name != ResponseHeadersAnnotation {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(unknown) != 0 {
		return options, reject("unknown bridge Ingress annotations: %s", strings.Join(unknown, ", "))
	}
	if value, present := annotations[HTTPSRedirectAnnotation]; present {
		switch value {
		case "true":
			options.redirect = true
		case "false":
		default:
			return options, reject("%s must be true or false", HTTPSRedirectAnnotation)
		}
	}
	if value, present := annotations[RedirectStatusAnnotation]; present {
		if !options.redirect {
			return options, reject("%s requires https-redirect: true", RedirectStatusAnnotation)
		}
		switch value {
		case "301":
			options.status = 301
		case "302":
			options.status = 302
		default:
			return options, reject("%s must be 301 or 302", RedirectStatusAnnotation)
		}
	}
	for _, key := range []string{RequestHeadersAnnotation, ResponseHeadersAnnotation} {
		value, present := annotations[key]
		if !present {
			continue
		}
		var input headerFilterInput
		strictErrors, err := strictjson.UnmarshalStrict([]byte(value), &input)
		headers, valid := input.headers()
		if err != nil || len(strictErrors) != 0 || !valid || !validHeaders(headers) {
			return options, reject("%s must be a valid JSON header filter with set/add/remove fields", key)
		}
		filter := gatewayv1.HTTPRouteFilter{Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier, RequestHeaderModifier: &headers}
		if key == ResponseHeadersAnnotation {
			filter = gatewayv1.HTTPRouteFilter{Type: gatewayv1.HTTPRouteFilterResponseHeaderModifier, ResponseHeaderModifier: &headers}
		}
		options.filters = append(options.filters, filter)
	}
	return options, nil
}

func (input headerFilterInput) headers() (gatewayv1.HTTPHeaderFilter, bool) {
	result := gatewayv1.HTTPHeaderFilter{Remove: input.Remove}
	for index, list := range [][]headerInput{input.Set, input.Add} {
		for _, header := range list {
			if header.Value == nil {
				return result, false
			}
			value := gatewayv1.HTTPHeader{Name: gatewayv1.HTTPHeaderName(header.Name), Value: *header.Value}
			if index == 0 {
				result.Set = append(result.Set, value)
			} else {
				result.Add = append(result.Add, value)
			}
		}
	}
	return result, true
}

func validHeaders(headers gatewayv1.HTTPHeaderFilter) bool {
	if len(headers.Set) > 16 || len(headers.Add) > 16 || len(headers.Remove) > 16 ||
		len(headers.Set)+len(headers.Add)+len(headers.Remove) == 0 {
		return false
	}
	seen := map[string]bool{}
	validName := func(name string) bool {
		lower := strings.ToLower(name)
		if len(name) > 256 || !httpguts.ValidHeaderFieldName(name) || seen[lower] {
			return false
		}
		seen[lower] = true
		return true
	}
	for _, list := range [][]gatewayv1.HTTPHeader{headers.Set, headers.Add} {
		for _, header := range list {
			if !validName(string(header.Name)) || len(header.Value) > 4096 || !httpguts.ValidHeaderFieldValue(header.Value) {
				return false
			}
		}
	}
	for _, name := range headers.Remove {
		if !validName(name) {
			return false
		}
	}
	return true
}
