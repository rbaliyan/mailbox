package rules

import (
	"regexp"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

// customFunctions returns CEL environment options that register convenience
// functions on top of the built-in map and list operators.
//
// Usage in rule conditions:
//
//	matches_regex(subject, "(?i)urgent")
//	has_header(headers, "X-Priority")
//	header_value(headers, "X-Priority") == "1"
//	has_metadata(metadata, "tenant_id")
//	metadata_value(metadata, "score") > 5
//	contains_tag(tags, "vip")
func customFunctions() []cel.EnvOption {
	return []cel.EnvOption{
		// matches_regex(s, pattern) bool
		// Returns true when s matches the regular expression.
		// Invalid patterns return false. Alias for the built-in s.matches(pattern).
		cel.Function("matches_regex",
			cel.Overload("matches_regex_string_string",
				[]*cel.Type{cel.StringType, cel.StringType},
				cel.BoolType,
				cel.BinaryBinding(func(s, pattern ref.Val) ref.Val {
					str, ok1 := s.(types.String)
					pat, ok2 := pattern.(types.String)
					if !ok1 || !ok2 {
						return types.Bool(false)
					}
					matched, err := regexp.MatchString(string(pat), string(str))
					if err != nil {
						return types.Bool(false)
					}
					return types.Bool(matched)
				}),
			),
		),

		// has_header(headers, key) bool
		// Returns true when the headers map contains key.
		// Equivalent to: key in headers
		cel.Function("has_header",
			cel.Overload("has_header_map_string",
				[]*cel.Type{cel.MapType(cel.StringType, cel.StringType), cel.StringType},
				cel.BoolType,
				cel.BinaryBinding(func(hdrs, key ref.Val) ref.Val {
					m, ok := hdrs.(traits.Mapper)
					if !ok {
						return types.Bool(false)
					}
					k, ok := key.(types.String)
					if !ok {
						return types.Bool(false)
					}
					return m.Contains(k)
				}),
			),
		),

		// header_value(headers, key) string
		// Returns headers[key] or "" when the key is absent.
		cel.Function("header_value",
			cel.Overload("header_value_map_string",
				[]*cel.Type{cel.MapType(cel.StringType, cel.StringType), cel.StringType},
				cel.StringType,
				cel.BinaryBinding(func(hdrs, key ref.Val) ref.Val {
					m, ok := hdrs.(traits.Mapper)
					if !ok {
						return types.String("")
					}
					k, ok := key.(types.String)
					if !ok {
						return types.String("")
					}
					val, found := m.Find(k)
					if !found || val == nil {
						return types.String("")
					}
					if s, ok := val.(types.String); ok {
						return s
					}
					return types.String("")
				}),
			),
		),

		// has_metadata(metadata, key) bool
		// Returns true when the metadata map contains key.
		// Equivalent to: key in metadata
		cel.Function("has_metadata",
			cel.Overload("has_metadata_map_string",
				[]*cel.Type{cel.MapType(cel.StringType, cel.DynType), cel.StringType},
				cel.BoolType,
				cel.BinaryBinding(func(meta, key ref.Val) ref.Val {
					m, ok := meta.(traits.Mapper)
					if !ok {
						return types.Bool(false)
					}
					k, ok := key.(types.String)
					if !ok {
						return types.Bool(false)
					}
					return m.Contains(k)
				}),
			),
		),

		// metadata_value(metadata, key) dyn
		// Returns metadata[key] or null when the key is absent.
		cel.Function("metadata_value",
			cel.Overload("metadata_value_map_string",
				[]*cel.Type{cel.MapType(cel.StringType, cel.DynType), cel.StringType},
				cel.DynType,
				cel.BinaryBinding(func(meta, key ref.Val) ref.Val {
					m, ok := meta.(traits.Mapper)
					if !ok {
						return types.NullValue
					}
					k, ok := key.(types.String)
					if !ok {
						return types.NullValue
					}
					val, found := m.Find(k)
					if !found || val == nil {
						return types.NullValue
					}
					return val
				}),
			),
		),

		// contains_tag(tags, tag) bool
		// Returns true when the tags list contains the given tag.
		// Equivalent to: tag in tags
		cel.Function("contains_tag",
			cel.Overload("contains_tag_list_string",
				[]*cel.Type{cel.ListType(cel.StringType), cel.StringType},
				cel.BoolType,
				cel.BinaryBinding(func(tagList, tag ref.Val) ref.Val {
					list, ok := tagList.(traits.Lister)
					if !ok {
						return types.Bool(false)
					}
					return list.Contains(tag)
				}),
			),
		),
	}
}
