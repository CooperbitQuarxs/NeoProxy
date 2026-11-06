package bedrockwire

import (
	"regexp"
	"strings"

	awsauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/aws"
)

// inferenceProfileGeoDefaultRegion is INFERENCE_PROFILE_GEO_DEFAULT_REGION:
// the region a geo-prefixed cross-region inference profile falls back to.
// global. profiles are anchored in the us regions and absent on purpose.
var inferenceProfileGeoDefaultRegion = map[string]string{
	"us":     "us-east-1",
	"us-gov": "us-gov-west-1",
	"eu":     "eu-west-1",
	"apac":   "ap-southeast-1",
	"au":     "ap-southeast-2",
	"jp":     "ap-northeast-1",
}

// awsRegionalHost is AWS_REGIONAL_BEDROCK_HOST, AWS's own regional runtime
// host, whose region segment is re-derived per request. FIPS, VPC-endpoint
// and gateway hosts do not match and are used as configured.
var awsRegionalHost = regexp.MustCompile(`^bedrock-runtime\.[a-z0-9-]+\.amazonaws\.com$`)

// arnRegion is inferRegionFromBedrockArn.
func arnRegion(modelID string) string {
	parts := strings.Split(modelID, ":")
	if len(parts) < 4 || parts[0] != "arn" || parts[2] != "bedrock" {
		return ""
	}
	return parts[3]
}

// inferenceProfileGeo is inferenceProfileGeo: eu.anthropic.… → eu.
func inferenceProfileGeo(modelID string) string {
	dot := strings.IndexByte(modelID, '.')
	if dot <= 0 {
		return ""
	}
	if _, ok := inferenceProfileGeoDefaultRegion[modelID[:dot]]; !ok {
		return ""
	}
	return modelID[:dot]
}

// regionServesGeo is regionServesGeo.
func regionServesGeo(region, geo string) bool {
	switch geo {
	case "us-gov":
		return strings.HasPrefix(region, "us-gov-")
	case "us":
		return strings.HasPrefix(region, "us-") && !strings.HasPrefix(region, "us-gov-")
	case "eu":
		return strings.HasPrefix(region, "eu-")
	case "apac":
		return strings.HasPrefix(region, "ap-")
	case "au":
		return region == "ap-southeast-2" || region == "ap-southeast-4"
	case "jp":
		return region == "ap-northeast-1" || region == "ap-northeast-3"
	}
	return false
}

// Region is resolveBedrockRegion as NeoPi's gateway reaches it: no region,
// profile or guardrail option is set, so the model ARN's region wins, then a
// geo-prefixed inference profile takes the ambient region only when it serves
// the profile's geo (else the geo default), and anything else takes the
// ambient region or us-east-1. ambient is awsauth.AmbientRegion("").
func Region(modelID, ambient string) string {
	if region := arnRegion(modelID); region != "" {
		return region
	}
	if geo := inferenceProfileGeo(modelID); geo != "" {
		if ambient != "" && regionServesGeo(ambient, geo) {
			return ambient
		}
		return inferenceProfileGeoDefaultRegion[geo]
	}
	if ambient != "" {
		return ambient
	}
	return awsauth.DefaultRegion
}

// DefaultBaseURL is the origin resolveBedrockBaseUrl uses for a row without
// a base URL.
func DefaultBaseURL(region string) string {
	return "https://bedrock-runtime." + region + ".amazonaws.com"
}

// Base is what new URL(baseUrl) yields for the request: the scheme, URL.host,
// URL.pathname and URL.search.
type Base struct {
	Scheme, Host, Path, Search string
}

// RequestURL is resolveBedrockBaseUrl then resolveBedrockRequestUrl for the
// converse-stream operation: AWS's regional host re-pointed at region, the
// base path without trailing slashes, the whole model id as one encoded
// segment, and the base query kept.
func RequestURL(base Base, region, modelID string) string {
	host := base.Host
	if awsRegionalHost.MatchString(host) {
		host = "bedrock-runtime." + region + ".amazonaws.com"
	}
	path := strings.TrimRight(base.Path, "/") + "/model/" + awsauth.EncodeURIComponent(modelID) + "/converse-stream"
	return base.Scheme + "://" + host + path + base.Search
}
