package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// keyHashPrefixLen is how much of a project's key_hash the default
// listing prints (BR-12: "short key-hash prefix") — enough that a
// handful of projects practically never collide, short enough to type.
const keyHashPrefixLen = 12

func newProjectsCmd() *cobra.Command {
	var relayFlag, watchKey, unwatchKey, watchNew, keepAwake string
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "List known projects and choose which ones report to your phone",
		Long: "With no flags, lists every project this bridge knows about. `--watch`/`--unwatch` " +
			"accept either a project's display name (when it is unambiguous) or a prefix of its " +
			"key shown in the listing; both forms are printed there so either can be copied.",
		Args:                  cobra.NoArgs,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjects(cmd.Context(), projectsDeps{
				out: cmd.OutOrStdout(), relayFlag: relayFlag,
				watchKey: watchKey, unwatchKey: unwatchKey, watchNew: watchNew, keepAwake: keepAwake,
			})
		},
	}
	cmd.Flags().StringVar(&relayFlag, "relay", "", "override the relay base URL (development only)")
	cmd.Flags().StringVar(&watchKey, "watch", "", "start reporting this project (display name or key prefix)")
	cmd.Flags().StringVar(&unwatchKey, "unwatch", "", "stop reporting this project (display name or key prefix)")
	cmd.Flags().StringVar(&watchNew, "watch-new", "", "on|off: whether a never-seen project starts out watched")
	cmd.Flags().StringVar(&keepAwake, "keep-awake", "",
		"on|off: keep the machine awake for the duration of a Claude Code session "+
			"(global, not per project; needs systemd-inhibit on Linux)")
	return cmd
}

type projectsDeps struct {
	out                                       io.Writer
	relayFlag                                 string
	watchKey, unwatchKey, watchNew, keepAwake string
	credStore                                 cred.Store
}

// runProjects dispatches to exactly one action: the flags are mutually
// exclusive in practice (a person runs "projects" to look, or runs it
// once per change), and the default with no flags at all is the listing.
func runProjects(ctx context.Context, d projectsDeps) error {
	switch {
	case d.watchKey != "":
		return applyWatchChange(ctx, d, d.watchKey, true)
	case d.unwatchKey != "":
		return applyWatchChange(ctx, d, d.unwatchKey, false)
	case d.watchNew != "":
		return applyWatchNew(ctx, d, d.watchNew)
	case d.keepAwake != "":
		return applyKeepAwake(d, d.keepAwake)
	default:
		wl := watch.Load(xdgpaths.WatchListPath())
		cfg, _ := config.Load(xdgpaths.ConfigPath())
		return printProjects(d, wl, cfg)
	}
}

func printProjects(d projectsDeps, wl watch.File, cfg config.Config) error {
	var hashes []string
	if wl.WatchList != nil {
		for h := range wl.WatchList.Projects {
			hashes = append(hashes, h)
		}
	}
	sort.Slice(hashes, func(i, j int) bool { return displayFor(wl, hashes[i]) < displayFor(wl, hashes[j]) })

	if len(hashes) == 0 {
		if _, err := fmt.Fprintln(d.out, "No known projects yet."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(d.out, "NAME\tKEY\tWATCHED\tLAST SEEN"); err != nil {
			return err
		}
		for _, h := range hashes {
			p := wl.WatchList.Projects[h]
			line := fmt.Sprintf("%s\t%s\t%s\t%s", displayFor(wl, h), shortHash(h), onOff(p.Watched), lastSeenLine(p.LastProjectSeenAt))
			if _, err := fmt.Fprintln(d.out, line); err != nil {
				return err
			}
		}
	}

	watchNewOn := true
	if wl.WatchList != nil {
		watchNewOn = wl.WatchList.WatchNewProjects
	}
	taskLabelOn := wl.Settings != nil && wl.Settings.TaskLabel
	if _, err := fmt.Fprintf(d.out, "\nWatch new projects: %s\nKeep-awake: %s\nTask labels: %s\n",
		onOff(watchNewOn), onOff(cfg.KeepAwake), onOff(taskLabelOn)); err != nil {
		return err
	}
	return nil
}

func displayFor(wl watch.File, hash string) string {
	if wl.WatchList != nil {
		if name := wl.WatchList.Projects[hash].DisplayName; name != "" {
			return name
		}
	}
	return "(unnamed)"
}

func shortHash(hash string) string {
	if len(hash) <= keyHashPrefixLen {
		return hash
	}
	return hash[:keyHashPrefixLen]
}

func lastSeenLine(rfc3339 string) string {
	if rfc3339 == "" {
		return "never"
	}
	return rfc3339
}

// resolveProjectKey implements BR-12's key resolution rule (stated
// explicitly here since the spec names only "key"): an exact,
// unambiguous display name wins; otherwise key is matched as a key_hash
// prefix, which must also be unambiguous. Ambiguity is an error naming
// every candidate so the person can copy the longer form from the
// listing instead.
func resolveProjectKey(wl watch.File, key string) (string, error) {
	if wl.WatchList == nil || len(wl.WatchList.Projects) == 0 {
		return "", fmt.Errorf("no known projects yet")
	}
	var byName, byPrefix []string
	for hash, p := range wl.WatchList.Projects {
		if p.DisplayName == key {
			byName = append(byName, hash)
		}
		if strings.HasPrefix(hash, key) {
			byPrefix = append(byPrefix, hash)
		}
	}
	switch {
	case len(byName) == 1:
		return byName[0], nil
	case len(byName) > 1:
		return "", fmt.Errorf("%q names more than one project: %s", key, strings.Join(candidateNames(wl, byName), ", "))
	case len(byPrefix) == 1:
		return byPrefix[0], nil
	case len(byPrefix) > 1:
		return "", fmt.Errorf("%q matches more than one project's key: %s", key, strings.Join(byPrefix, ", "))
	default:
		return "", fmt.Errorf("no known project matches %q", key)
	}
}

func candidateNames(wl watch.File, hashes []string) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = fmt.Sprintf("%s (%s)", displayFor(wl, h), shortHash(h))
	}
	return out
}

func applyWatchChange(ctx context.Context, d projectsDeps, key string, watched bool) error {
	wl := watch.Load(xdgpaths.WatchListPath())
	hash, err := resolveProjectKey(wl, key)
	if err != nil {
		return err
	}

	result, syncErr := setWatchAtRelay(ctx, d, relay.SetWatchRequest{
		Projects: map[string]relay.ProjectEntry{hash: {Watched: watched}},
	})
	// Prefer the relay's own answer (its version counter, its whole
	// list); fall back to an optimistic local-only edit when the sync
	// itself failed or the relay had nothing newer to say.
	if syncErr != nil || !wl.ApplyRelayList(result) {
		if wl.WatchList == nil {
			wl.WatchList = &watch.List{WatchNewProjects: true, Projects: map[string]watch.ProjectEntry{}}
		}
		entry := wl.WatchList.Projects[hash]
		entry.Watched = watched
		wl.WatchList.Projects[hash] = entry
	}
	if err := watch.Save(xdgpaths.WatchListPath(), wl); err != nil {
		return err
	}

	verb := "Watching"
	if !watched {
		verb = "No longer watching"
	}
	if _, err := fmt.Fprintf(d.out, "%s %s.\n", verb, displayFor(wl, hash)); err != nil {
		return err
	}
	return reportSync(d, syncErr)
}

func applyWatchNew(ctx context.Context, d projectsDeps, value string) error {
	on, err := parseOnOff(value)
	if err != nil {
		return err
	}
	wl := watch.Load(xdgpaths.WatchListPath())

	result, syncErr := setWatchAtRelay(ctx, d, relay.SetWatchRequest{WatchNewProjects: &on})
	if syncErr != nil || !wl.ApplyRelayList(result) {
		if wl.WatchList == nil {
			wl.WatchList = &watch.List{}
		}
		wl.WatchList.WatchNewProjects = on
	}
	if err := watch.Save(xdgpaths.WatchListPath(), wl); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(d.out, "Watch new projects: %s.\n", onOff(on)); err != nil {
		return err
	}
	return reportSync(d, syncErr)
}

// applyKeepAwake is BR-16's setting, entirely local (unlike watch/unwatch
// and watch-new, it is never sent to the relay — the phone has no
// opinion on whether this machine stays awake).
func applyKeepAwake(d projectsDeps, value string) error {
	on, err := parseOnOff(value)
	if err != nil {
		return err
	}
	cfg, _ := config.Load(xdgpaths.ConfigPath())
	cfg.KeepAwake = on
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(d.out, "Keep-awake: %s. This is a global setting, not per project.\n", onOff(on))
	return err
}

func parseOnOff(value string) (bool, error) {
	switch value {
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("want \"on\" or \"off\", got %q", value)
	}
}

// setWatchAtRelay resolves credentials and calls SetWatch, best-effort:
// any failure (unpaired, no credential, network, or a relay that has not
// deployed the endpoint yet) is returned as err so the caller falls back
// to applying the change locally only.
func setWatchAtRelay(ctx context.Context, d projectsDeps, req relay.SetWatchRequest) (*relay.WatchList, error) {
	cfg, cfgErr := config.Load(xdgpaths.ConfigPath())
	if cfgErr != nil || cfg.BridgeID == "" || cfg.BridgeID == config.UnpairedBridgeID {
		return nil, fmt.Errorf("not paired")
	}
	credStore := d.credStore
	if credStore == nil {
		credStore = cred.Default()
	}
	secret, err := credStore.Get()
	if err != nil || secret == "" {
		return nil, fmt.Errorf("no credential stored")
	}
	baseURL, err := resolveRelayBaseURL(d.relayFlag)
	if err != nil {
		return nil, err
	}
	client, err := relay.NewClient(relay.Config{BaseURL: baseURL, BridgeID: cfg.BridgeID, BridgeSecret: secret, Version: version})
	if err != nil {
		return nil, err
	}
	syncCtx, cancel := context.WithTimeout(ctx, statusHealthTimeout)
	defer cancel()
	return client.SetWatch(syncCtx, req)
}

func reportSync(d projectsDeps, syncErr error) error {
	if syncErr == nil {
		return nil
	}
	_, err := fmt.Fprintln(d.out, "Saved locally, not yet synced to your phone.")
	return err
}
