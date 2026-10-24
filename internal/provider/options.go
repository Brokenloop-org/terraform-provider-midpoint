package provider

import "slices"

// optionReevaluateSearchFilters makes midPoint resolve references that carry
// only a filter. The engine adds it to any write that sends such a
// reference, because the repository rejects them otherwise.
const optionReevaluateSearchFilters = "reevaluateSearchFilters"

// readExcludes are the heaviest server-managed items, left out of every read.
// Canonical drops them anyway; excluding them saves the transfer where
// midPoint honours the exclusion.
var readExcludes = []string{
	"metadata", "operationExecution", "roleMembershipRef", "linkRef", "fetchResult", "operationalStateHistory",
}

// membersSearchOptions are sent with the members check before a delete:
// "raw" makes it a repository search. midPoint 4.9 refuses a provisioning
// search for shadows across a whole resource ("Object type or object class
// must be specified"), and a count needs no post-processing.
var membersSearchOptions = []string{"raw"}

// membersSearchLimit is how many referencing objects the members check
// fetches; their names go into the error message.
const membersSearchLimit = 5

// effectiveOptions returns the midPoint execution options for one write:
// the resource's options attribute when set (non-nil, even empty: an empty
// list sends no options), else the provider's default_options entry for the
// resource short name when present, else the built-in defaults. The result
// is a fresh slice the caller may append to. "overwrite" is never added
// here; it is sent only when the user lists it.
func effectiveOptions(defaults map[string][]string, short string, resourceOpts []string, builtin []string) []string {
	if resourceOpts != nil {
		return slices.Clone(resourceOpts)
	}
	if opts, ok := defaults[short]; ok {
		if opts == nil {
			return []string{}
		}
		return slices.Clone(opts)
	}
	return slices.Clone(builtin)
}

// withOption returns opts with option appended when it is not already
// present.
func withOption(opts []string, option string) []string {
	if slices.Contains(opts, option) {
		return opts
	}
	return append(opts, option)
}
