package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/corruptmem/msx/internal/auth"
	"github.com/corruptmem/msx/internal/graph"
	"github.com/corruptmem/msx/internal/store"
)

type globalFlags struct {
	profile string
	format  string
}

var errHelpShown = errors.New("help shown")

const usageText = `usage: msx [--profile name] [--format text|json] <command> [flags]

commands:
  login         Start device-code login and save tokens locally
  import-op     Import an existing Microsoft account from 1Password
  profiles      List configured profiles
  whoami        Show the current Graph account
  mail          List mail with optional filters
  agenda        List calendar events in a time range
  files         List or search OneDrive files
  contacts      List or search contacts
  sites         Search SharePoint / org sites
  help          Show this message
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errHelpShown) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	g, rest, err := parseGlobals(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return showUsage(true, "")
	}
	if rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		return showUsage(false, "")
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	defer s.Close()

	cmd := rest[0]
	rest = rest[1:]
	switch cmd {
	case "login":
		return cmdLogin(s, g, rest)
	case "import-op":
		return cmdImportOP(s, g, rest)
	case "profiles":
		return cmdProfiles(s, g, rest)
	case "whoami":
		return cmdWhoami(s, g, rest)
	case "mail":
		return cmdMail(s, g, rest)
	case "agenda":
		return cmdAgenda(s, g, rest)
	case "files":
		return cmdFiles(s, g, rest)
	case "contacts":
		return cmdContacts(s, g, rest)
	case "sites":
		return cmdSites(s, g, rest)
	default:
		return showUsage(true, fmt.Sprintf("unknown command %q", cmd))
	}
}

func parseGlobals(args []string) (globalFlags, []string, error) {
	g := globalFlags{profile: "default", format: "json"}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--profile":
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("--profile requires a value")
			}
			g.profile = args[i+1]
			i++
		case strings.HasPrefix(a, "--profile="):
			g.profile = strings.TrimPrefix(a, "--profile=")
		case a == "--format":
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("--format requires a value")
			}
			g.format = args[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			g.format = strings.TrimPrefix(a, "--format=")
		default:
			out = append(out, a)
		}
	}
	if g.format != "text" && g.format != "json" {
		return g, nil, fmt.Errorf("--format must be text or json")
	}
	return g, out, nil
}

func showUsage(asError bool, prefix string) error {
	if asError {
		if prefix != "" {
			return fmt.Errorf("%s\n\n%s", prefix, usageText)
		}
		return fmt.Errorf(usageText)
	}
	if prefix != "" {
		fmt.Fprintln(os.Stdout, prefix)
		fmt.Fprintln(os.Stdout)
	}
	_, _ = fmt.Fprint(os.Stdout, usageText)
	return errHelpShown
}

func cmdLogin(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	clientID := fs.String("client-id", "", "Azure app client ID")
	authority := fs.String("authority", "common", "tenant/common/organizations/consumers")
	scopesRaw := fs.String("scopes", strings.Join(auth.DefaultScopes, ","), "comma-separated scopes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *clientID == "" {
		return fmt.Errorf("--client-id is required")
	}
	scopes := splitCSV(*scopesRaw)
	flow, err := auth.BeginDeviceLogin(*clientID, *authority, scopes)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, flow.Message)
	token, err := auth.FinishDeviceLogin(*clientID, *authority, flow)
	if err != nil {
		return err
	}
	profile := store.Profile{Name: g.profile, Authority: *authority, ClientID: *clientID, Scopes: scopes}
	if err := s.SaveProfileAndToken(profile, token); err != nil {
		return err
	}
	return emit(g, map[string]any{"ok": true, "profile": g.profile, "authority": *authority})
}

func cmdImportOP(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("import-op", flag.ContinueOnError)
	accountItem := fs.String("account-item", "", "1Password item title holding the account refresh token")
	appItem := fs.String("app-item", "MS Graph App", "1Password item title holding the shared app registration")
	vault := fs.String("vault", "Claw", "1Password vault")
	scopesRaw := fs.String("scopes", strings.Join(auth.DefaultScopes, ","), "comma-separated scopes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *accountItem == "" {
		return fmt.Errorf("--account-item is required")
	}
	if err := auth.ImportFrom1Password(s, g.profile, *accountItem, *appItem, *vault, splitCSV(*scopesRaw)); err != nil {
		return err
	}
	return emit(g, map[string]any{"ok": true, "profile": g.profile, "source": "1password", "account_item": *accountItem})
}

func cmdProfiles(s *store.Store, g globalFlags, _ []string) error {
	profiles, err := s.ListProfiles()
	if err != nil {
		return err
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	rows := make([]map[string]any, 0, len(profiles))
	for _, p := range profiles {
		tok, _ := s.GetToken(p.Name)
		rows = append(rows, map[string]any{
			"name":          p.Name,
			"authority":     p.Authority,
			"account_email": p.AccountEmail,
			"scopes":        p.Scopes,
			"expires_at":    tok.ExpiresAt,
		})
	}
	return emit(g, rows)
}

func cmdWhoami(s *store.Store, g globalFlags, _ []string) error {
	data, err := graph.Client{Store: s, Profile: g.profile}.Request("GET", "/me", nil)
	if err != nil {
		return err
	}
	return emit(g, data)
}

func cmdMail(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("mail", flag.ContinueOnError)
	top := fs.Int("top", 10, "maximum number of messages")
	sender := fs.String("sender", "", "exact sender email filter")
	query := fs.String("query", "", "Graph search expression text")
	subject := fs.String("subject", "", "case-insensitive subject substring filter applied client-side")
	since := fs.String("since", "", "received since RFC3339 timestamp")
	folder := fs.String("folder", "inbox", "well-known mail folder or folder id")
	unread := fs.Bool("unread", false, "only include unread messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	path := fmt.Sprintf("/me/mailFolders/%s/messages", *folder)
	q := map[string]string{"$top": fmt.Sprint(*top), "$select": "id,subject,receivedDateTime,from,isRead,webLink"}
	filters := []string{}
	if *sender != "" {
		filters = append(filters, fmt.Sprintf("from/emailAddress/address eq '%s'", strings.ReplaceAll(*sender, "'", "''")))
	}
	if *since != "" {
		if _, err := time.Parse(time.RFC3339, *since); err != nil {
			return fmt.Errorf("invalid --since, want RFC3339: %w", err)
		}
		filters = append(filters, fmt.Sprintf("receivedDateTime ge %s", *since))
	}
	if *unread {
		filters = append(filters, "isRead eq false")
	}
	if len(filters) > 0 {
		q["$filter"] = strings.Join(filters, " and ")
	}
	if *query != "" {
		q["$search"] = fmt.Sprintf("\"%s\"", *query)
	} else {
		q["$orderby"] = "receivedDateTime desc"
	}
	data, err := graph.Client{Store: s, Profile: g.profile}.Request("GET", path, q)
	if err != nil {
		return err
	}
	if *subject != "" {
		data["value"] = filterMailBySubject(data["value"], *subject)
	}
	return emit(g, data)
}

func cmdAgenda(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("agenda", flag.ContinueOnError)
	top := fs.Int("top", 20, "maximum number of events")
	start := fs.String("start", time.Now().UTC().Format(time.RFC3339), "range start RFC3339")
	end := fs.String("end", time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339), "range end RFC3339")
	query := fs.String("query", "", "search text applied client-side to subject/location/organizer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	startTime, err := time.Parse(time.RFC3339, *start)
	if err != nil {
		return fmt.Errorf("invalid --start, want RFC3339: %w", err)
	}
	endTime, err := time.Parse(time.RFC3339, *end)
	if err != nil {
		return fmt.Errorf("invalid --end, want RFC3339: %w", err)
	}
	if !endTime.After(startTime) {
		return fmt.Errorf("--end must be after --start")
	}
	data, err := graph.Client{Store: s, Profile: g.profile}.Request("GET", "/me/calendarView", map[string]string{
		"startDateTime": *start,
		"endDateTime":   *end,
		"$top":          fmt.Sprint(*top),
		"$orderby":      "start/dateTime",
		"$select":       "id,subject,start,end,location,organizer,webLink",
	})
	if err != nil {
		return err
	}
	if *query != "" {
		data["value"] = filterEvents(data["value"], *query)
	}
	return emit(g, data)
}

func cmdFiles(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("files", flag.ContinueOnError)
	top := fs.Int("top", 25, "maximum number of items")
	path := fs.String("path", "", "folder path to list")
	query := fs.String("query", "", "search query")
	kind := fs.String("kind", "all", "all|files|folders")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	if *kind != "all" && *kind != "files" && *kind != "folders" {
		return fmt.Errorf("--kind must be all, files, or folders")
	}
	endpoint := "/me/drive/root/children"
	params := map[string]string{"$top": fmt.Sprint(*top), "$select": "id,name,webUrl,file,folder,size,lastModifiedDateTime,parentReference"}
	if *query != "" {
		endpoint = fmt.Sprintf("/me/drive/root/search(q='%s')", escapePathSegment(*query))
	} else if *path != "" {
		endpoint = fmt.Sprintf("/me/drive/root:/%s:/children", strings.TrimPrefix(*path, "/"))
	}
	data, err := graph.Client{Store: s, Profile: g.profile}.Request("GET", endpoint, params)
	if err != nil {
		return err
	}
	if *kind != "all" {
		data["value"] = filterDriveItems(data["value"], *kind)
	}
	return emit(g, data)
}

func cmdContacts(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("contacts", flag.ContinueOnError)
	top := fs.Int("top", 20, "maximum number of contacts")
	query := fs.String("query", "", "display name/email prefix filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	params := map[string]string{"$top": fmt.Sprint(*top), "$select": "id,displayName,emailAddresses,mobilePhone,businessPhones", "$orderby": "displayName"}
	if *query != "" {
		safe := strings.ReplaceAll(*query, "'", "''")
		params["$filter"] = fmt.Sprintf("startswith(displayName,'%s') or emailAddresses/any(e:startswith(e/address,'%s'))", safe, safe)
	}
	data, err := graph.Client{Store: s, Profile: g.profile}.Request("GET", "/me/contacts", params)
	if err != nil {
		return err
	}
	return emit(g, data)
}

func cmdSites(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("sites", flag.ContinueOnError)
	top := fs.Int("top", 10, "maximum number of sites")
	query := fs.String("query", "", "site search query")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	if *query == "" {
		return fmt.Errorf("--query is required")
	}
	data, err := graph.Client{Store: s, Profile: g.profile}.Request("GET", "/sites", map[string]string{"search": *query, "$top": fmt.Sprint(*top)})
	if err != nil {
		return err
	}
	return emit(g, data)
}

func emit(g globalFlags, v any) error {
	if g.format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(b))
	return err
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func escapePathSegment(v string) string {
	return strings.ReplaceAll(v, "'", "''")
}

func filterMailBySubject(v any, q string) []map[string]any {
	return filterRows(v, func(row map[string]any) bool {
		subject, _ := row["subject"].(string)
		return strings.Contains(strings.ToLower(subject), strings.ToLower(q))
	})
}

func filterEvents(v any, q string) []map[string]any {
	q = strings.ToLower(q)
	return filterRows(v, func(row map[string]any) bool {
		blob, _ := json.Marshal(row)
		return strings.Contains(strings.ToLower(string(blob)), q)
	})
}

func filterDriveItems(v any, kind string) []map[string]any {
	return filterRows(v, func(row map[string]any) bool {
		_, hasFile := row["file"]
		_, hasFolder := row["folder"]
		switch kind {
		case "files":
			return hasFile && !hasFolder
		case "folders":
			return hasFolder
		default:
			return true
		}
	})
}

func filterRows(v any, keep func(map[string]any) bool) []map[string]any {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []map[string]any{}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if keep(row) {
			out = append(out, row)
		}
	}
	return out
}

func requirePositive(name string, v int) error {
	if v <= 0 {
		return fmt.Errorf("%s must be > 0", name)
	}
	return nil
}
