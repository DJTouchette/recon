package recon

import (
	"path/filepath"
	"strings"
)

// resolvePath maps a caller-supplied path onto the repo-relative,
// slash-separated key the index actually uses.
//
// Every path-taking entry point needs this because callers do not have the
// repo-relative form to hand. An agent gets a path from a file listing, an
// editor, or a previous tool result, and what it has is absolute; a Windows
// caller has backslashes. Neither matched, and the failure was not a failure:
// Context on an absolute path returned a fully-formed record reading
// fan_in 0, fan_out 0, churn 0 for a file with fan-in 8, which is the single
// most actionable wrong answer recon can give — "nothing depends on this, it
// is safe to change".
//
// The candidates are tried in order and the first one that is actually indexed
// wins. Nothing is rewritten speculatively: a path that already resolves is
// returned untouched, so no currently-working call can change meaning. When
// none resolve, the input is returned cleaned, and callers report it as
// unknown rather than as empty — see Recon.Indexed.
func (r *Recon) resolvePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if r.idx != nil && r.idx.Get(p) != nil {
		return p
	}

	for _, cand := range r.pathCandidates(p) {
		if cand != "" && r.idx != nil && r.idx.Get(cand) != nil {
			return cand
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// pathCandidates lists the repo-relative forms a caller-supplied path might
// correspond to, most likely first.
func (r *Recon) pathCandidates(p string) []string {
	var out []string
	add := func(s string) {
		if s == "" || s == "." {
			return
		}
		s = filepath.ToSlash(filepath.Clean(s))
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}

	// A backslash is a legal filename character on Unix, so this is offered as
	// a candidate and never applied unconditionally — it only wins if the
	// converted form is the one that is actually indexed.
	slashed := strings.ReplaceAll(p, `\`, "/")

	for _, form := range []string{p, slashed} {
		add(form)

		// Absolute → relative to the repo root. filepath.Rel also produces
		// "../.." escapes for paths outside the repo; those are dropped,
		// because a path outside the repo is genuinely not indexed and
		// pretending otherwise would be the same lie in a new place.
		if filepath.IsAbs(form) || strings.HasPrefix(form, "/") {
			if rel, err := filepath.Rel(r.root, filepath.FromSlash(form)); err == nil {
				rel = filepath.ToSlash(rel)
				if !strings.HasPrefix(rel, "../") && rel != ".." {
					add(rel)
				}
			}
		}
	}
	return out
}

// Indexed reports whether a caller-supplied path corresponds to a file recon
// actually scanned.
//
// This is the difference between "this file has no dependents" and "recon has
// never seen this file", which every zero-valued metric otherwise conflates.
func (r *Recon) Indexed(path string) bool {
	if r.idx == nil {
		return false
	}
	return r.idx.Get(r.resolvePath(path)) != nil
}
