package main

import "strings"

// OR in a "/" search: GitHub's project filter has none (a comma ORs values
// of one qualifier, but qualifiers always AND), so CardHub runs each branch
// of "assignee:@me OR no:assignee" (or "|") as its own query and merges the
// cards (see FetchColumns).

// maxOr is the most branches a search may have: an exact total takes a query
// per mix of branches, 2ⁿ−1 of them.
const maxOr = 3

// orBranches splits a filter at OR and | outside double quotes; a filter
// without them is its only branch. Empty branches are dropped.
func orBranches(f string) []string {
	var spaced strings.Builder // "a|b" → "a | b", so | needs no spaces around it
	quoted := false
	for _, r := range f {
		if r == '"' {
			quoted = !quoted
		}
		if r == '|' && !quoted {
			spaced.WriteString(" | ")
			continue
		}
		spaced.WriteRune(r)
	}
	var branches, cur []string
	end := func() {
		if len(cur) > 0 {
			branches = append(branches, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, tok := range splitFilter(spaced.String()) {
		if tok == "OR" || tok == "|" {
			end()
			continue
		}
		cur = append(cur, tok)
	}
	end()
	if len(branches) == 0 {
		return []string{strings.TrimSpace(f)}
	}
	return branches
}

// andFilter narrows every branch of search by the view's filter.
func andFilter(view, search string) string {
	bs := orBranches(search)
	if len(bs) < 2 {
		return strings.TrimSpace(view + " " + search)
	}
	for i := range bs {
		bs[i] = strings.TrimSpace(view + " " + bs[i])
	}
	return strings.Join(bs, " OR ")
}
