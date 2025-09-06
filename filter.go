package pg_oui

import "regexp"

// Filter restricts the generated dataset to a subset of vendors/OUIs.
// The filtering is applied at build time if auto-update is triggered (when
// built with the 'oui_runtime_update' build tag), or by external tools.
type Filter struct {
    VendorNames []string      // simplified names (LLC/Ltd/Inc removed)
    VendorRegex *regexp.Regexp
    OUIs        []string // strings like 0CB4A4 or 00:11:22
}

