// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

// Command release cuts release tags for faros components.
//
// Each component has its own tag namespace and independent version line:
//
//	hub             v<X.Y.Z>                          repo-wide release: goreleaser
//	                                                  CLI + hub/agent images +
//	                                                  platform charts (faros-hub,
//	                                                  faros-agent)
//	quickstart      providers/quickstart/v<X.Y.Z>     provider-release.yaml builds
//	                                                  the image + chart stamped with
//	                                                  this version; the source is
//	                                                  also mirrored to faroshq/
//	                                                  provider-<name> (source only)
//	infrastructure  providers/infrastructure/v<X.Y.Z>
//	code            providers/code/v<X.Y.Z>
//
// It finds the component's latest existing tag, bumps it (patch by default),
// and creates + pushes the new tag — the release workflows do the rest.
//
// Usage:
//
//	release <component|all> [flags]
//
//	release current               # print every component's latest tag
//	release quickstart            # bump providers/quickstart/v* patch and push
//	release hub --minor           # bump v* minor
//	release quickstart --tag v0.0.1   # explicit version
//	release all --dry-run         # preview every component's next tag
//	release all --tag v0.1.0      # put every component on the same version
//
// Flags:
//
//	--tag <vX.Y.Z>   set the exact version; with 'all', every component gets it
//	                 (must be ahead of each targeted component's latest tag)
//	--minor          bump the minor (default: patch)
//	--major          bump the major
//	--ref <commit>   commit/ref to tag (default: HEAD)
//	--dry-run        print the plan, create nothing
//	-y, --yes        don't prompt for confirmation before pushing
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// component describes a release line: its tag prefix (the version is appended
// directly, e.g. prefix "v" + "0.0.73" = "v0.0.73") and a one-line summary of
// what cutting the tag sets in motion — shown in the plan so a dry-run makes the
// downstream effect obvious. Order in componentOrder matters for `all`.
type component struct {
	prefix   string
	triggers string
}

// provider-sdk is first: the providers depend on it, so when releasing `all`
// the SDK tag is cut (and published to the mirror) before the providers that
// will eventually `require` that published version.
var componentOrder = []string{"provider-sdk", "hub", "quickstart", "kuery", "app-studio", "infrastructure", "code", "edges", "databricks", "agents"}

var components = map[string]component{
	"provider-sdk":   {"provider-sdk/v", "split → faroshq/provider-sdk; publishes the go-gettable SDK module (providers require this version once the replace is dropped)"},
	"hub":            {"v", "goreleaser CLI release + hub/agent images + platform Helm charts (ghcr.io/faroshq)"},
	"quickstart":     {"providers/quickstart/v", "provider-release.yaml builds the image + chart at this version; source mirror → faroshq/provider-quickstart"},
	"kuery":          {"providers/kuery/v", "provider-release.yaml builds the image + chart at this version; source mirror → faroshq/provider-kuery"},
	"app-studio":     {"providers/app-studio/v", "provider-release.yaml builds the image + chart at this version; source mirror → faroshq/provider-app-studio"},
	"infrastructure": {"providers/infrastructure/v", "provider-release.yaml builds the image + chart at this version; source mirror → faroshq/provider-infrastructure"},
	"code":           {"providers/code/v", "provider-release.yaml builds the image + chart at this version; source mirror → faroshq/provider-code"},
	"edges":          {"providers/edges/v", "provider-release.yaml builds the image (ghcr.io/faroshq/faros-edges-provider) + chart at this version; source mirror → faroshq/provider-edges"},
	"databricks":     {"providers/databricks/v", "provider-release.yaml builds the image (ghcr.io/faroshq/faros-databricks-provider) + chart at this version; source mirror → faroshq/provider-databricks"},
	"agents":         {"providers/agents/v", "provider-release.yaml builds the image (ghcr.io/faroshq/faros-agents-provider) + chart at this version; no source mirror"},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type options struct {
	tag    string // explicit version override (e.g. "v0.0.1")
	bump   string // "patch" | "minor" | "major"
	ref    string // commit/ref to tag
	dryRun bool
	yes    bool
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage()
		return nil
	}
	target := args[0]
	if target == "current" {
		return printCurrent()
	}
	opts := options{bump: "patch", ref: "HEAD"}

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--tag":
			i++
			if i >= len(args) {
				return fmt.Errorf("--tag needs a value")
			}
			opts.tag = args[i]
		case "--minor":
			opts.bump = "minor"
		case "--major":
			opts.bump = "major"
		case "--ref":
			i++
			if i >= len(args) {
				return fmt.Errorf("--ref needs a value")
			}
			opts.ref = args[i]
		case "--dry-run":
			opts.dryRun = true
		case "-y", "--yes":
			opts.yes = true
		default:
			return fmt.Errorf("unknown flag %q (try --help)", args[i])
		}
	}

	// Resolve the target component set.
	var names []string
	if target == "all" {
		// `all --tag vX.Y.Z` deliberately puts every component on the SAME
		// version — the way to re-align independently drifted release lines at
		// a common milestone. Without --tag each line bumps from its own latest.
		names = componentOrder
	} else {
		if _, ok := components[target]; !ok {
			return fmt.Errorf("unknown component %q; valid: all, %s", target, strings.Join(componentOrder, ", "))
		}
		names = []string{target}
	}

	commit, err := gitOut("rev-parse", "--short", opts.ref)
	if err != nil {
		return fmt.Errorf("resolving ref %q: %w", opts.ref, err)
	}
	branch, _ := gitOut("rev-parse", "--abbrev-ref", "HEAD")

	var explicit version
	if opts.tag != "" {
		v, ok := parseVersion(opts.tag)
		if !ok {
			return fmt.Errorf("invalid --tag %q (want vMAJOR.MINOR.PATCH[-pre])", opts.tag)
		}
		explicit = v
	}

	// Tags that already exist. Tagging is all-or-nothing: every component is
	// vetted against both sets before anything is created, so a collision on
	// the last component of `all` can't surface after the first nine are
	// pushed. The remote lookup is best effort — offline, localTags still
	// catches the common case.
	localTags, err := existingTags()
	if err != nil {
		return err
	}
	remoteTags := existingRemoteTags()

	// Build the plan, collecting every component's problems rather than
	// failing on the first — with `all` you want the full list in one go.
	type plan struct{ name, from, fullTag, triggers string }
	var plans []plan
	var problems []string
	for _, name := range names {
		comp := components[name]
		latest, hasLatest, err := latestTag(comp.prefix)
		if err != nil {
			return err
		}

		from := "(none)"
		if hasLatest {
			from = comp.prefix + strings.TrimPrefix(latest.String(), "v")
		}

		var next version
		switch {
		case opts.tag != "":
			// An explicit version must move every targeted line forward.
			if hasLatest && !less(latest, explicit) {
				problems = append(problems, fmt.Sprintf("%-15s already at %s — %s is not ahead of it", name, from, opts.tag))
				continue
			}
			next = explicit
		case hasLatest:
			next = bump(latest, opts.bump)
		default:
			next = version{0, 0, 1, ""} // first release
		}

		full := comp.prefix + strings.TrimPrefix(next.String(), "v")
		switch {
		case localTags[full]:
			problems = append(problems, fmt.Sprintf("%-15s %s already exists locally", name, full))
			continue
		case remoteTags[full]:
			problems = append(problems, fmt.Sprintf("%-15s %s already exists on origin", name, full))
			continue
		}
		plans = append(plans, plan{name, from, full, comp.triggers})
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d of %d component(s) cannot be tagged — nothing was created:\n  %s",
			len(problems), len(names), strings.Join(problems, "\n  "))
	}

	// Show the plan: the version step and what each tag sets in motion.
	fmt.Printf("Tagging commit %s (%s):\n\n", commit, branch)
	for _, p := range plans {
		fmt.Printf("  %-15s %s  ->  %s\n", p.name, p.from, p.fullTag)
		fmt.Printf("  %-15s   ↳ %s\n", "", p.triggers)
	}
	fmt.Println()

	if opts.dryRun {
		fmt.Println("dry-run — would run:")
		for _, p := range plans {
			fmt.Printf("  git tag %s %s && git push origin %s\n", p.fullTag, opts.ref, p.fullTag)
		}
		return nil
	}

	if !opts.yes {
		ok, err := confirm(fmt.Sprintf("Create and push %d tag(s)? [y/N] ", len(plans)))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted.")
			return nil
		}
	}

	// Create the tags locally first; only push once all are created so a bad
	// version doesn't leave a half-pushed set.
	for _, p := range plans {
		if err := gitRun("tag", p.fullTag, opts.ref); err != nil {
			return fmt.Errorf("creating tag %s: %w", p.fullTag, err)
		}
	}
	for _, p := range plans {
		if err := gitRun("push", "origin", p.fullTag); err != nil {
			return fmt.Errorf("pushing tag %s: %w (other tags were created locally; `git push origin <tag>` to retry)", p.fullTag, err)
		}
		fmt.Printf("pushed %s\n", p.fullTag)
	}
	fmt.Println("\nDone — the release workflows will pick these up.")
	return nil
}

// printCurrent prints the latest existing tag for every component, in
// componentOrder, so you can see each release line's current version at a
// glance without cutting anything.
func printCurrent() error {
	fmt.Println("Current latest versions:")
	fmt.Println()
	for _, name := range componentOrder {
		comp := components[name]
		latest, hasLatest, err := latestTag(comp.prefix)
		if err != nil {
			return err
		}
		current := "(none)"
		if hasLatest {
			current = comp.prefix + strings.TrimPrefix(latest.String(), "v")
		}
		fmt.Printf("  %-15s %s\n", name, current)
	}
	return nil
}

// existingTags returns every tag in the local repository, as a set.
func existingTags() (map[string]bool, error) {
	out, err := gitOut("tag", "-l")
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}
	return tagSet(out, ""), nil
}

// existingRemoteTags returns the tags on origin, as a set. A tag that exists
// only on the remote would fail at push time — after earlier tags in the same
// run were already pushed — so it's worth one network round-trip to catch.
// Returns nil when origin is unreachable: an offline dry-run is more useful
// than a hard failure, and `git push` still refuses the tag either way.
func existingRemoteTags() map[string]bool {
	out, err := gitOut("ls-remote", "--tags", "origin")
	if err != nil {
		return nil
	}
	return tagSet(out, "refs/tags/")
}

// tagSet parses tag names out of git output, one per line. Lines may carry a
// leading "<sha>\t" (ls-remote) and refs may be suffixed "^{}" (the annotated
// tag's dereferenced commit); both are stripped. Only names carrying trim as a
// prefix are kept, with that prefix removed.
func tagSet(out, trim string) map[string]bool {
	set := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		name := strings.TrimSpace(line)
		if _, after, found := strings.Cut(name, "\t"); found {
			name = after
		}
		name = strings.TrimSuffix(name, "^{}")
		if name == "" || !strings.HasPrefix(name, trim) {
			continue
		}
		set[strings.TrimPrefix(name, trim)] = true
	}
	return set
}

// latestTag returns the highest semver tag carrying prefix, with the prefix
// stripped. hasLatest is false when no matching tag exists.
func latestTag(prefix string) (version, bool, error) {
	out, err := gitOut("tag", "-l", prefix+"*")
	if err != nil {
		return version{}, false, fmt.Errorf("listing tags %q: %w", prefix+"*", err)
	}
	var vs []version
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, prefix) {
			continue
		}
		// Guard against prefix bleed: "v" must not match "providers/...". The
		// HasPrefix check covers that, but for the bare "v" prefix also require
		// the remainder to be a version, which parseVersion enforces below.
		if v, ok := parseVersion("v" + strings.TrimPrefix(line, prefix)); ok {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return version{}, false, nil
	}
	sort.Slice(vs, func(i, j int) bool { return less(vs[i], vs[j]) })
	return vs[len(vs)-1], true, nil
}

// version is a parsed semver (vMAJOR.MINOR.PATCH[-prerelease]).
type version struct {
	major, minor, patch int
	pre                 string
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre := s, ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, pre = s[:i], s[i+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		nums[i] = n
	}
	return version{nums[0], nums[1], nums[2], pre}, true
}

func (v version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch)
	if v.pre != "" {
		s += "-" + v.pre
	}
	return s
}

// less orders versions; a release (no prerelease) outranks its prereleases.
func less(a, b version) bool {
	switch {
	case a.major != b.major:
		return a.major < b.major
	case a.minor != b.minor:
		return a.minor < b.minor
	case a.patch != b.patch:
		return a.patch < b.patch
	case a.pre == b.pre:
		return false
	case a.pre == "":
		return false // a is the release, ranks above b's prerelease
	case b.pre == "":
		return true
	default:
		return a.pre < b.pre
	}
}

// bump increments part and drops any prerelease, so a release tag follows from
// the latest version's core (e.g. v0.0.1-rc1 -> patch -> v0.0.2).
func bump(v version, part string) version {
	switch part {
	case "major":
		return version{v.major + 1, 0, 0, ""}
	case "minor":
		return version{v.major, v.minor + 1, 0, ""}
	default:
		return version{v.major, v.minor, v.patch + 1, ""}
	}
}

func confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, nil // EOF / no tty -> treat as no
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func gitRun(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func usage() {
	fmt.Print(`release — cut release tags for faros components

Usage:
  release <component|all> [flags]

Components:
  provider-sdk    provider-sdk/v<X.Y.Z>             (split → faroshq/provider-sdk)
  hub             v<X.Y.Z>                          (repo-wide release)
  quickstart      providers/quickstart/v<X.Y.Z>
  kuery           providers/kuery/v<X.Y.Z>
  app-studio      providers/app-studio/v<X.Y.Z>
  infrastructure  providers/infrastructure/v<X.Y.Z>
  code            providers/code/v<X.Y.Z>
  edges           providers/edges/v<X.Y.Z>
  databricks      providers/databricks/v<X.Y.Z>
  agents          providers/agents/v<X.Y.Z>
  all             every component (independent versions, or one shared --tag)
  current         print every component's latest existing tag (no changes)

Flags:
  --tag <vX.Y.Z>   set the exact version; with 'all', every component gets it
                   (must be ahead of each targeted component's latest tag)
  --minor          bump the minor (default: patch)
  --major          bump the major
  --ref <commit>   commit/ref to tag (default: HEAD)
  --dry-run        print the plan, create nothing
  -y, --yes        skip the confirmation prompt

Examples:
  release current               print every component's latest tag
  release quickstart            bump providers/quickstart/v* patch and push
  release hub --minor           bump v* minor
  release quickstart --tag v0.0.1
  release all --dry-run
  release all --tag v0.1.0     put every component on the same version
`)
}
