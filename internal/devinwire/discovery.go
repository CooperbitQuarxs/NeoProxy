package devinwire

import (
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// CliModelConfigsPath is the unary RPC NeoPi's Devin discovery calls.
const CliModelConfigsPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

// BunUserAgent is Bun's default fetch identity: NeoPi's discovery and usage
// go through plain fetch, without its inference headers.
const BunUserAgent = "Bun/1.3.14"

// Display options (exa.codeium_common_pb.DisplayOption). The vendored NeoPi
// descriptor stops at QUICK_REVIEW; 6-8 are the native client's extra slots.
const (
	DisplayUnspecified     int32 = 0
	DisplayModelRouter     int32 = 3
	DisplayQuickReview     int32 = 4
	DisplayInternalDefault int32 = 6
	DisplayUnclassified    int32 = 7
	DisplayNormal          int32 = 8
)

// SupportedModelDisplays are the slots NeoPi advertises so the server returns
// its full catalog; the internal ones are filtered client-side.
var SupportedModelDisplays = []int32{DisplayModelRouter, DisplayQuickReview, DisplayInternalDefault, DisplayUnclassified, DisplayNormal}

// EncodeCliModelConfigsRequest is GetCliModelConfigsRequest{metadata}.
func EncodeCliModelConfigsRequest(metadata Metadata) []byte {
	return appendMessage(nil, 1, metadata.encode())
}

// Model dimension kinds (exa.codeium_common_pb.ModelDimensionKind).
const (
	DimensionCost      int32 = 1
	DimensionCostFuzzy int32 = 2
)

// ModelDimension is exa.codeium_common_pb.ModelDimension's rate fields.
type ModelDimension struct {
	Label       string
	Value       float64 // proto float, widened like protobuf-es
	Denominator string
	Kind        int32
}

// ModelFeatures is exa.codeium_common_pb.ModelFeatures.
type ModelFeatures struct {
	SupportsImages, SupportsToolCalls, SupportsParallelToolCalls, SupportsThinking bool
}

// ModelInfo is the exa.codeium_common_pb.ModelInfo fields discovery reads.
type ModelInfo struct {
	MaxTokens       int32
	Features        *ModelFeatures
	MaxOutputTokens int32
	HarnessUIDs     []string
	DisplayOption   int32
	IsModelRouter   bool
}

// FamilyValue is exa.codeium_common_pb.ModelFamilyMetadataValue.
type FamilyValue struct {
	Order int32
	Name  string
}

// FamilyEntry is exa.codeium_common_pb.ModelFamilyMetadataEntry.
type FamilyEntry struct {
	Key   string
	Value *FamilyValue
}

// FamilyMetadata is exa.codeium_common_pb.ModelFamilyMetadata.
type FamilyMetadata struct {
	Label                  string
	Entries                []FamilyEntry
	IsDefaultModelInFamily bool
}

// ClientModelConfig is the exa.codeium_common_pb.ClientModelConfig fields
// discovery reads.
type ClientModelConfig struct {
	Label                  string
	ModelUID               string
	Disabled               bool
	SupportsImages         bool
	MaxTokens              int32
	Info                   *ModelInfo
	Family                 *FamilyMetadata
	IsDefaultModelInFamily bool
	Dimensions             []ModelDimension
}

// CliModelConfigs is GetCliModelConfigsResponse: the account's configs and
// the model uid its plan starts on.
type CliModelConfigs struct {
	Configs    []ClientModelConfig
	DefaultUID string
}

// DecodeCliModelConfigs decodes a GetCliModelConfigs response.
func DecodeCliModelConfigs(b []byte) (CliModelConfigs, error) {
	var out CliModelConfigs
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			config, err := decodeClientModelConfig(value)
			if err != nil {
				return err
			}
			out.Configs = append(out.Configs, config)
		case 3:
			return fields(value, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
				if num == 3 && typ == protowire.BytesType {
					out.DefaultUID = string(value)
				}
				return nil
			})
		}
		return nil
	})
	return out, err
}

func decodeClientModelConfig(b []byte) (ClientModelConfig, error) {
	var out ClientModelConfig
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case typ == protowire.BytesType && num == 1:
			out.Label = string(value)
		case typ == protowire.BytesType && num == 22:
			out.ModelUID = string(value)
		case typ == protowire.VarintType && num == 4:
			out.Disabled = varint != 0
		case typ == protowire.VarintType && num == 5:
			out.SupportsImages = varint != 0
		case typ == protowire.VarintType && num == 18:
			out.MaxTokens = int32(varint)
		case typ == protowire.BytesType && num == 23:
			info, err := decodeModelInfo(value)
			if err != nil {
				return err
			}
			out.Info = &info
		case typ == protowire.BytesType && num == 30:
			family, err := decodeFamilyMetadata(value)
			if err != nil {
				return err
			}
			out.Family = &family
		case typ == protowire.VarintType && num == 31:
			out.IsDefaultModelInFamily = varint != 0
		case typ == protowire.BytesType && num == 32:
			var dimension ModelDimension
			if err := fields(value, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
				switch {
				case typ == protowire.BytesType && num == 1:
					dimension.Label = string(value)
				case typ == protowire.Fixed32Type && num == 2:
					dimension.Value = float64(math.Float32frombits(uint32(varint)))
				case typ == protowire.BytesType && num == 3:
					dimension.Denominator = string(value)
				case typ == protowire.VarintType && num == 6:
					dimension.Kind = int32(varint)
				}
				return nil
			}); err != nil {
				return err
			}
			out.Dimensions = append(out.Dimensions, dimension)
		}
		return nil
	})
	return out, err
}

func decodeModelInfo(b []byte) (ModelInfo, error) {
	var out ModelInfo
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case typ == protowire.VarintType && num == 4:
			out.MaxTokens = int32(varint)
		case typ == protowire.BytesType && num == 6:
			features := ModelFeatures{}
			if err := fields(value, func(num protowire.Number, typ protowire.Type, _ []byte, varint uint64) error {
				if typ != protowire.VarintType {
					return nil
				}
				switch num {
				case 11:
					features.SupportsImages = varint != 0
				case 12:
					features.SupportsToolCalls = varint != 0
				case 15:
					features.SupportsThinking = varint != 0
				case 21:
					features.SupportsParallelToolCalls = varint != 0
				}
				return nil
			}); err != nil {
				return err
			}
			out.Features = &features
		case typ == protowire.VarintType && num == 13:
			out.MaxOutputTokens = int32(varint)
		case typ == protowire.BytesType && num == 20:
			out.HarnessUIDs = append(out.HarnessUIDs, string(value))
		case typ == protowire.VarintType && num == 22:
			out.DisplayOption = int32(varint)
		case typ == protowire.VarintType && num == 25:
			out.IsModelRouter = varint != 0
		}
		return nil
	})
	return out, err
}

func decodeFamilyMetadata(b []byte) (FamilyMetadata, error) {
	var out FamilyMetadata
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case typ == protowire.BytesType && num == 1:
			out.Label = string(value)
		case typ == protowire.BytesType && num == 2:
			var entry FamilyEntry
			if err := fields(value, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
				switch {
				case typ == protowire.BytesType && num == 1:
					entry.Key = string(value)
				case typ == protowire.BytesType && num == 2:
					entryValue := FamilyValue{}
					if err := fields(value, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
						switch {
						case typ == protowire.VarintType && num == 1:
							entryValue.Order = int32(varint)
						case typ == protowire.BytesType && num == 2:
							entryValue.Name = string(value)
						}
						return nil
					}); err != nil {
						return err
					}
					entry.Value = &entryValue
				}
				return nil
			}); err != nil {
				return err
			}
			out.Entries = append(out.Entries, entry)
		case typ == protowire.VarintType && num == 3:
			out.IsDefaultModelInFamily = varint != 0
		}
		return nil
	})
	return out, err
}
