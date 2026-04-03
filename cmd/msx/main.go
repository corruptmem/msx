package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

var (
	errHelpShown = errors.New("help shown")
	version      = "dev"
	commit       = "unknown"
	buildDate    = "unknown"
)

const usageText = `usage: msx [--profile name] [--format text|json] <command> [flags]

commands:
  login            Start device-code login and save tokens locally
  import-op        Import an existing Microsoft account from 1Password
  state-export     Export one or all local profiles as JSON backup
  state-import     Import profile state from a JSON backup
  version          Show CLI version/build provenance
  profiles         List configured profiles
  whoami           Show the current Graph account
  mail             List mail with optional filters
  mail-get         Fetch one mail message by id
  agenda           List calendar events in a time range
  event-get        Fetch one calendar event by id
  files            List or search OneDrive files
  file-get         Fetch one OneDrive item by id
  folder-list      List contents of a OneDrive folder by item ID
  file-download    Download a OneDrive file to a local path
  file-move        Move a OneDrive item to a new parent folder
  folder-create    Create a folder in OneDrive
  contacts         List or search contacts
  contact-get      Fetch one contact by id
  sites            Search SharePoint / org sites
  site-get         Fetch one site by id
  next             Continue from a returned @odata.nextLink URL
  help             Show this message
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
	case "state-export":
		return cmdStateExport(s, g, rest)
	case "state-import":
		return cmdStateImport(s, g, rest)
	case "version":
		return cmdVersion(g, rest)
	case "profiles":
		return cmdProfiles(s, g, rest)
	case "whoami":
		return cmdWhoami(s, g, rest)
	case "mail":
		return cmdMail(s, g, rest)
	case "mail-get":
		return cmdMailGet(s, g, rest)
	case "agenda":
		return cmdAgenda(s, g, rest)
	case "event-get":
		return cmdEventGet(s, g, rest)
	case "files":
		return cmdFiles(s, g, rest)
	case "file-get":
		return cmdFileGet(s, g, rest)
	case "folder-list":
		return cmdFolderList(s, g, rest)
	case "file-download":
		return cmdFileDownload(s, g, rest)
	case "file-move":
		return cmdFileMove(s, g, rest)
	case "folder-create":
		return cmdFolderCreate(s, g, rest)
	case "contacts":
		return cmdContacts(s, g, rest)
	case "contact-get":
		return cmdContactGet(s, g, rest)
	case "sites":
		return cmdSites(s, g, rest)
	case "site-get":
		return cmdSiteGet(s, g, rest)
	case "next":
		return cmdNext(s, g, rest)
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
	return emit(g, "login", map[string]any{"ok": true, "profile": g.profile, "authority": *authority})
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
	return emit(g, "import-op", map[string]any{"ok": true, "profile": g.profile, "source": "1password", "account_item": *accountItem})
}

func cmdStateExport(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("state-export", flag.ContinueOnError)
	all := fs.Bool("all", false, "export all configured profiles")
	outPath := fs.String("out", "-", "output path, or - for stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: msx state-export [--all] [--out path|-]")
	}
	var (
		backup store.StateBackup
		err    error
	)
	if *all {
		backup, err = s.ExportAllProfiles()
	} else {
		backup, err = s.ExportProfile(g.profile)
	}
	if err != nil {
		return err
	}
	payload, err := store.MarshalStateBackup(backup)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if *outPath == "-" {
		_, err = os.Stdout.Write(payload)
		return err
	}
	if err := writeFileAtomically(*outPath, payload, 0o600); err != nil {
		return err
	}
	return emit(g, "state-export", map[string]any{
		"ok":       true,
		"path":     *outPath,
		"count":    len(backup.Profiles),
		"profiles": backupProfileNames(backup),
	})
}

func cmdStateImport(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("state-import", flag.ContinueOnError)
	inPath := fs.String("in", "-", "input path, or - for stdin")
	overwrite := fs.Bool("overwrite", false, "overwrite existing profiles from the backup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: msx state-import [--in path|-] [--overwrite]")
	}
	payload, err := readInput(*inPath)
	if err != nil {
		return err
	}
	backup, err := store.ParseStateBackup(payload)
	if err != nil {
		return err
	}
	if err := s.ImportStateBackup(backup, *overwrite); err != nil {
		return err
	}
	return emit(g, "state-import", map[string]any{
		"ok":        true,
		"source":    *inPath,
		"count":     len(backup.Profiles),
		"profiles":  backupProfileNames(backup),
		"overwrite": *overwrite,
	})
}

func cmdVersion(g globalFlags, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: msx version")
	}
	return emit(g, "version", map[string]any{
		"version":    version,
		"commit":     commit,
		"build_date": buildDate,
	})
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
	return emit(g, "profiles", rows)
}

func cmdWhoami(s *store.Store, g globalFlags, _ []string) error {
	data, err := newGraphClient(s, g.profile).Request("GET", "/me", nil)
	if err != nil {
		return err
	}
	return emit(g, "whoami", data)
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
	nextLink := fs.String("next-link", "", "continue from a returned @odata.nextLink URL")
	tzName := fs.String("tz", "UTC", "IANA timezone for receivedDateTime output (e.g. Europe/London)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	var (
		data map[string]any
		err  error
	)
	if *nextLink != "" {
		if err := validateNextLink(*nextLink); err != nil {
			return err
		}
		data, err = newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	} else {
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
		data, err = newGraphClient(s, g.profile).Request("GET", path, q)
	}
	if err != nil {
		return err
	}
	if *subject != "" {
		data["value"] = filterMailBySubject(data["value"], *subject)
	}
	loc, err := parseLocation(*tzName)
	if err != nil {
		return err
	}
	convertMailTZ(data["value"], loc)
	return emit(g, "mail", data)
}

func cmdMailGet(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("mail-get", flag.ContinueOnError)
	body := fs.Bool("body", false, "include body and bodyPreview fields")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx mail-get [--body] <message-id>")
	}
	selectFields := []string{"id", "subject", "receivedDateTime", "sentDateTime", "from", "toRecipients", "ccRecipients", "bccRecipients", "isRead", "conversationId", "hasAttachments", "importance", "webLink"}
	if *body {
		selectFields = append(selectFields, "bodyPreview", "body")
	}
	data, err := newGraphClient(s, g.profile).Request("GET", "/me/messages/"+url.PathEscape(fs.Arg(0)), map[string]string{"$select": strings.Join(selectFields, ",")})
	if err != nil {
		return err
	}
	return emit(g, "mail-get", data)
}

func cmdAgenda(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("agenda", flag.ContinueOnError)
	top := fs.Int("top", 20, "maximum number of events")
	start := fs.String("start", time.Now().UTC().Format(time.RFC3339), "range start RFC3339")
	end := fs.String("end", time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339), "range end RFC3339")
	query := fs.String("query", "", "search text applied client-side to subject/location/organizer")
	nextLink := fs.String("next-link", "", "continue from a returned @odata.nextLink URL")
	tzName := fs.String("tz", "UTC", "IANA timezone for start/end dateTime output (e.g. Europe/London). Note: Graph returns event times in the calendar's own timezone; --tz converts what is stored in the dateTime field.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	var (
		data map[string]any
		err  error
	)
	if *nextLink != "" {
		if err := validateNextLink(*nextLink); err != nil {
			return err
		}
		data, err = newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	} else {
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
		data, err = newGraphClient(s, g.profile).Request("GET", "/me/calendarView", map[string]string{
			"startDateTime": *start,
			"endDateTime":   *end,
			"$top":          fmt.Sprint(*top),
			"$orderby":      "start/dateTime",
			"$select":       "id,subject,start,end,location,organizer,webLink",
		})
	}
	if err != nil {
		return err
	}
	if *query != "" {
		data["value"] = filterEvents(data["value"], *query)
	}
	loc, err := parseLocation(*tzName)
	if err != nil {
		return err
	}
	convertAgendaTZ(data["value"], loc)
	return emit(g, "agenda", data)
}

func cmdEventGet(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("event-get", flag.ContinueOnError)
	body := fs.Bool("body", false, "include body and bodyPreview fields")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx event-get [--body] <event-id>")
	}
	selectFields := []string{"id", "subject", "start", "end", "location", "locations", "organizer", "attendees", "isAllDay", "showAs", "sensitivity", "webLink", "onlineMeeting", "onlineMeetingUrl"}
	if *body {
		selectFields = append(selectFields, "bodyPreview", "body")
	}
	data, err := newGraphClient(s, g.profile).Request("GET", "/me/events/"+url.PathEscape(fs.Arg(0)), map[string]string{"$select": strings.Join(selectFields, ",")})
	if err != nil {
		return err
	}
	return emit(g, "event-get", data)
}

func cmdFiles(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("files", flag.ContinueOnError)
	top := fs.Int("top", 25, "maximum number of items")
	path := fs.String("path", "", "folder path to list")
	query := fs.String("query", "", "search query")
	kind := fs.String("kind", "all", "all|files|folders")
	nextLink := fs.String("next-link", "", "continue from a returned @odata.nextLink URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	if *kind != "all" && *kind != "files" && *kind != "folders" {
		return fmt.Errorf("--kind must be all, files, or folders")
	}
	var (
		data map[string]any
		err  error
	)
	if *nextLink != "" {
		if err := validateNextLink(*nextLink); err != nil {
			return err
		}
		data, err = newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	} else {
		endpoint := "/me/drive/root/children"
		params := map[string]string{"$top": fmt.Sprint(*top), "$select": "id,name,webUrl,file,folder,size,lastModifiedDateTime,parentReference"}
		if *query != "" {
			endpoint = fmt.Sprintf("/me/drive/root/search(q='%s')", escapePathSegment(*query))
		} else if *path != "" {
			endpoint = fmt.Sprintf("/me/drive/root:/%s:/children", strings.TrimPrefix(*path, "/"))
		}
		data, err = newGraphClient(s, g.profile).Request("GET", endpoint, params)
	}
	if err != nil {
		return err
	}
	if *kind != "all" {
		data["value"] = filterDriveItems(data["value"], *kind)
	}
	return emit(g, "files", data)
}

func cmdFileGet(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("file-get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx file-get <drive-item-id>")
	}
	data, err := newGraphClient(s, g.profile).Request("GET", "/me/drive/items/"+url.PathEscape(fs.Arg(0)), map[string]string{"$select": "id,name,webUrl,file,folder,size,lastModifiedDateTime,createdDateTime,parentReference,shared,fileSystemInfo,@microsoft.graph.downloadUrl"})
	if err != nil {
		return err
	}
	return emit(g, "file-get", data)
}

func cmdFolderList(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("folder-list", flag.ContinueOnError)
	top := fs.Int("top", 200, "maximum number of items")
	nextLink := fs.String("next-link", "", "continue from a returned @odata.nextLink URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 && *nextLink == "" {
		return fmt.Errorf("usage: msx folder-list [--top N] <drive-item-id>")
	}
	var (
		data map[string]any
		err  error
	)
	if *nextLink != "" {
		if err := validateNextLink(*nextLink); err != nil {
			return err
		}
		data, err = newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	} else {
		folderID := fs.Arg(0)
		data, err = newGraphClient(s, g.profile).Request("GET",
			"/me/drive/items/"+url.PathEscape(folderID)+"/children",
			map[string]string{
				"$top":    fmt.Sprint(*top),
				"$select": "id,name,webUrl,file,folder,size,lastModifiedDateTime,parentReference",
			})
	}
	if err != nil {
		return err
	}
	return emit(g, "files", data)
}

func cmdFileDownload(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("file-download", flag.ContinueOnError)
	out := fs.String("out", "", "local output path (default: filename in current directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx file-download [--out path] <drive-item-id>")
	}
	itemID := fs.Arg(0)

	// First fetch item metadata to get the name
	meta, err := newGraphClient(s, g.profile).Request("GET", "/me/drive/items/"+url.PathEscape(itemID),
		map[string]string{"$select": "id,name,size"})
	if err != nil {
		return err
	}
	name, _ := meta["name"].(string)
	if name == "" {
		name = itemID
	}
	size, _ := meta["size"].(float64)

	outPath := *out
	if outPath == "" {
		outPath = name
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	// Use the /content endpoint which redirects to a download URL — follow redirect
	// Get a fresh token using the already-open store
	token, err := auth.RefreshIfNeeded(s, g.profile, 5*time.Minute)
	if err != nil {
		return err
	}

	contentURL := "https://graph.microsoft.com/v1.0/me/drive/items/" + url.PathEscape(itemID) + "/content"
	req, err := http.NewRequest("GET", contentURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("User-Agent", "msx/0")

	httpClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed %d: %s", resp.StatusCode, string(body))
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	return emit(g, "file-download", map[string]any{
		"ok":      true,
		"item_id": itemID,
		"name":    name,
		"path":    outPath,
		"bytes":   written,
		"size":    int64(size),
	})
}

func cmdFileMove(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("file-move", flag.ContinueOnError)
	parentID := fs.String("parent-id", "", "destination folder drive-item ID")
	newName := fs.String("name", "", "rename item (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx file-move --parent-id <folder-id> [--name new-name] <drive-item-id>")
	}
	if *parentID == "" {
		return fmt.Errorf("--parent-id is required")
	}
	itemID := fs.Arg(0)
	body := map[string]any{
		"parentReference": map[string]string{"id": *parentID},
	}
	if *newName != "" {
		body["name"] = *newName
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data, err := newGraphClient(s, g.profile).RequestWithBody("PATCH", "/me/drive/items/"+url.PathEscape(itemID), nil, bodyJSON)
	if err != nil {
		return err
	}
	return emit(g, "file-move", data)
}

func cmdFolderCreate(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("folder-create", flag.ContinueOnError)
	parentID := fs.String("parent-id", "", "parent folder drive-item ID (default: drive root)")
	parentPath := fs.String("parent-path", "", "parent folder path e.g. 'Documents/Personal Admin'")
	conflict := fs.String("conflict", "fail", "behaviour if folder exists: fail|rename|replace")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx folder-create [--parent-id id | --parent-path path] [--conflict fail|rename|replace] <folder-name>")
	}
	folderName := fs.Arg(0)
	var endpoint string
	switch {
	case *parentID != "":
		endpoint = "/me/drive/items/" + url.PathEscape(*parentID) + "/children"
	case *parentPath != "":
		endpoint = "/me/drive/root:/" + strings.TrimPrefix(*parentPath, "/") + ":/children"
	default:
		endpoint = "/me/drive/root/children"
	}
	body := map[string]any{
		"name":                              folderName,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": *conflict,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data, err := newGraphClient(s, g.profile).RequestWithBody("POST", endpoint, nil, bodyJSON)
	if err != nil {
		return err
	}
	return emit(g, "folder-create", data)
}

func cmdContacts(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("contacts", flag.ContinueOnError)
	top := fs.Int("top", 20, "maximum number of contacts")
	query := fs.String("query", "", "display name/email prefix filter")
	nextLink := fs.String("next-link", "", "continue from a returned @odata.nextLink URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	var (
		data map[string]any
		err  error
	)
	if *nextLink != "" {
		if err := validateNextLink(*nextLink); err != nil {
			return err
		}
		data, err = newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	} else {
		params := map[string]string{"$top": fmt.Sprint(*top), "$select": "id,displayName,emailAddresses,mobilePhone,businessPhones", "$orderby": "displayName"}
		if *query != "" {
			safe := strings.ReplaceAll(*query, "'", "''")
			params["$filter"] = fmt.Sprintf("startswith(displayName,'%s') or emailAddresses/any(e:startswith(e/address,'%s'))", safe, safe)
		}
		data, err = newGraphClient(s, g.profile).Request("GET", "/me/contacts", params)
	}
	if err != nil {
		return err
	}
	return emit(g, "contacts", data)
}

func cmdContactGet(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("contact-get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx contact-get <contact-id>")
	}
	data, err := newGraphClient(s, g.profile).Request("GET", "/me/contacts/"+url.PathEscape(fs.Arg(0)), map[string]string{"$select": "id,displayName,givenName,surname,companyName,jobTitle,emailAddresses,businessPhones,homePhones,mobilePhone,officeLocation,birthday,personalNotes,categories,imAddresses,homeAddress,businessAddress,otherAddress,parentFolderId,createdDateTime,lastModifiedDateTime"})
	if err != nil {
		return err
	}
	return emit(g, "contact-get", data)
}

func cmdSites(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("sites", flag.ContinueOnError)
	top := fs.Int("top", 10, "maximum number of sites")
	query := fs.String("query", "", "site search query")
	nextLink := fs.String("next-link", "", "continue from a returned @odata.nextLink URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requirePositive("--top", *top); err != nil {
		return err
	}
	var (
		data map[string]any
		err  error
	)
	if *nextLink != "" {
		if err := validateNextLink(*nextLink); err != nil {
			return err
		}
		data, err = newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	} else {
		if *query == "" {
			return fmt.Errorf("--query is required")
		}
		data, err = newGraphClient(s, g.profile).Request("GET", "/sites", map[string]string{"search": *query, "$top": fmt.Sprint(*top)})
	}
	if err != nil {
		return err
	}
	return emit(g, "sites", data)
}

func cmdSiteGet(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("site-get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: msx site-get <site-id>")
	}
	data, err := newGraphClient(s, g.profile).Request("GET", "/sites/"+url.PathEscape(fs.Arg(0)), map[string]string{"$select": "id,name,displayName,description,webUrl,createdDateTime,lastModifiedDateTime,sharepointIds,siteCollection,root,parentReference"})
	if err != nil {
		return err
	}
	return emit(g, "site-get", data)
}

func cmdNext(s *store.Store, g globalFlags, args []string) error {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	nextLink := fs.String("url", "", "the @odata.nextLink URL to fetch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nextLink == "" {
		return fmt.Errorf("--url is required")
	}
	if err := validateNextLink(*nextLink); err != nil {
		return err
	}
	data, err := newGraphClient(s, g.profile).RequestURL("GET", *nextLink)
	if err != nil {
		return err
	}
	return emit(g, "next", data)
}

func newGraphClient(s *store.Store, profile string) graph.Client {
	client := graph.Client{Store: s, Profile: profile}
	if baseURL := os.Getenv("MSX_GRAPH_BASE_URL"); baseURL != "" {
		client.BaseURL = strings.TrimRight(baseURL, "/")
	}
	return client
}

func emit(g globalFlags, command string, v any) error {
	if g.format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	if text, ok := renderText(command, v); ok {
		_, err := fmt.Fprintln(os.Stdout, text)
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(b))
	return err
}

func renderText(command string, v any) (string, bool) {
	switch command {
	case "version":
		row, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("version: %s\ncommit: %s\nbuild_date: %s", stringValue(row, "version"), stringValue(row, "commit"), stringValue(row, "build_date")), true
	case "profiles":
		rows, ok := v.([]map[string]any)
		if !ok {
			return "", false
		}
		if len(rows) == 0 {
			return "no profiles configured", true
		}
		var b strings.Builder
		for i, row := range rows {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "%s\n  authority: %s", stringValue(row, "name"), stringValue(row, "authority"))
			if email := stringValue(row, "account_email"); email != "" {
				fmt.Fprintf(&b, "\n  account: %s", email)
			}
			if scopes := stringSliceValue(row, "scopes"); len(scopes) > 0 {
				fmt.Fprintf(&b, "\n  scopes: %s", strings.Join(scopes, ", "))
			}
			if expiresAt := int64Value(row, "expires_at"); expiresAt > 0 {
				fmt.Fprintf(&b, "\n  expires: %s", time.Unix(expiresAt, 0).UTC().Format(time.RFC3339))
			}
		}
		return b.String(), true
	case "whoami":
		row, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		identity := firstNonEmpty(stringValue(row, "displayName"), stringValue(row, "userPrincipalName"), stringValue(row, "mail"))
		secondary := firstNonEmpty(stringValue(row, "mail"), stringValue(row, "userPrincipalName"))
		var b strings.Builder
		b.WriteString(identity)
		if secondary != "" && secondary != identity {
			fmt.Fprintf(&b, "\nemail: %s", secondary)
		}
		for _, field := range []struct{ label, key string }{{"id", "id"}, {"job_title", "jobTitle"}, {"office", "officeLocation"}, {"mobile", "mobilePhone"}, {"preferred_language", "preferredLanguage"}} {
			if value := stringValue(row, field.key); value != "" {
				fmt.Fprintf(&b, "\n%s: %s", field.label, value)
			}
		}
		return b.String(), true
	case "mail":
		return renderMailList(v)
	case "agenda":
		return renderAgendaList(v)
	case "files":
		return renderFilesList(v)
	default:
		return "", false
	}
}

func renderMailList(v any) (string, bool) {
	data, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	items := filterRows(data["value"], func(map[string]any) bool { return true })
	if len(items) == 0 {
		return withNextLink("no messages", data), true
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		status := "read"
		if read, ok := item["isRead"].(bool); ok && !read {
			status = "unread"
		}
		fmt.Fprintf(&b, "%d. [%s] %s  %s", i+1, status, stringValue(item, "receivedDateTime"), firstNonEmpty(stringValue(nestedMap(item, "from", "emailAddress"), "address"), "(unknown sender)"))
		fmt.Fprintf(&b, "\n   %s", firstNonEmpty(stringValue(item, "subject"), "(no subject)"))
		if link := stringValue(item, "webLink"); link != "" {
			fmt.Fprintf(&b, "\n   %s", link)
		}
	}
	return withNextLink(b.String(), data), true
}

func renderAgendaList(v any) (string, bool) {
	data, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	items := filterRows(data["value"], func(map[string]any) bool { return true })
	if len(items) == 0 {
		return withNextLink("no events", data), true
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. %s → %s  %s", i+1, stringValue(nestedMap(item, "start"), "dateTime"), stringValue(nestedMap(item, "end"), "dateTime"), firstNonEmpty(stringValue(item, "subject"), "(no subject)"))
		if location := stringValue(nestedMap(item, "location"), "displayName"); location != "" {
			fmt.Fprintf(&b, "\n   location: %s", location)
		}
		if organizer := stringValue(nestedMap(item, "organizer", "emailAddress"), "address"); organizer != "" {
			fmt.Fprintf(&b, "\n   organizer: %s", organizer)
		}
		if link := stringValue(item, "webLink"); link != "" {
			fmt.Fprintf(&b, "\n   %s", link)
		}
	}
	return withNextLink(b.String(), data), true
}

func renderFilesList(v any) (string, bool) {
	data, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	items := filterRows(data["value"], func(map[string]any) bool { return true })
	if len(items) == 0 {
		return withNextLink("no files", data), true
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		kind := "file"
		if _, ok := item["folder"]; ok {
			kind = "folder"
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, kind, firstNonEmpty(stringValue(item, "name"), "(unnamed)"))
		if size, ok := item["size"]; ok {
			fmt.Fprintf(&b, "\n   size: %v", size)
		}
		if modified := stringValue(item, "lastModifiedDateTime"); modified != "" {
			fmt.Fprintf(&b, "\n   modified: %s", modified)
		}
		if parentPath := stringValue(nestedMap(item, "parentReference"), "path"); parentPath != "" {
			fmt.Fprintf(&b, "\n   parent: %s", parentPath)
		}
		if link := stringValue(item, "webUrl"); link != "" {
			fmt.Fprintf(&b, "\n   %s", link)
		}
	}
	return withNextLink(b.String(), data), true
}

func withNextLink(body string, data map[string]any) string {
	if next := stringValue(data, "@odata.nextLink"); next != "" {
		return body + "\n\nnext: " + next
	}
	return body
}

func nestedMap(row map[string]any, keys ...string) map[string]any {
	cur := row
	for _, key := range keys {
		value, ok := cur[key].(map[string]any)
		if !ok {
			return nil
		}
		cur = value
	}
	return cur
}

func stringValue(row map[string]any, key string) string {
	if row == nil {
		return ""
	}
	v, _ := row[key].(string)
	return v
}

func stringSliceValue(row map[string]any, key string) []string {
	if row == nil {
		return nil
	}
	items, ok := row[key].([]string)
	if ok {
		return items
	}
	vals, ok := row[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, item := range vals {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func int64Value(row map[string]any, key string) int64 {
	if row == nil {
		return 0
	}
	switch v := row[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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

func validateNextLink(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid --next-link URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("--next-link must use https")
	}
	if !strings.EqualFold(u.Host, "graph.microsoft.com") {
		return fmt.Errorf("--next-link host must be graph.microsoft.com")
	}
	return nil
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

// parseLocation loads an IANA timezone by name. "UTC" is the zero-value default.
func parseLocation(name string) (*time.Location, error) {
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	return loc, nil
}

// convertTZ parses a datetime string (RFC3339 or Graph's truncated RFC3339)
// and returns it formatted in loc with an explicit numeric offset.
func convertTZ(loc *time.Location, s string) string {
	if s == "" || loc == time.UTC {
		return s
	}
	// Graph sometimes omits the trailing Z or offset; try a few layouts.
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.In(loc).Format(time.RFC3339)
		}
	}
	return s // leave unchanged if unparseable
}

// convertMailTZ rewrites receivedDateTime fields in-place.
func convertMailTZ(v any, loc *time.Location) {
	if loc == time.UTC {
		return
	}
	items, ok := v.([]map[string]any)
	if !ok {
		return
	}
	for _, item := range items {
		if s, ok := item["receivedDateTime"].(string); ok {
			item["receivedDateTime"] = convertTZ(loc, s)
		}
	}
}

// convertAgendaTZ rewrites start.dateTime and end.dateTime in-place.
func convertAgendaTZ(v any, loc *time.Location) {
	if loc == time.UTC {
		return
	}
	items, ok := v.([]map[string]any)
	if !ok {
		return
	}
	for _, item := range items {
		for _, key := range []string{"start", "end"} {
			if nested, ok := item[key].(map[string]any); ok {
				if s, ok := nested["dateTime"].(string); ok {
					nested["dateTime"] = convertTZ(loc, s)
				}
			}
		}
	}
}

func requirePositive(name string, v int) error {
	if v <= 0 {
		return fmt.Errorf("%s must be > 0", name)
	}
	return nil
}

func backupProfileNames(backup store.StateBackup) []string {
	names := make([]string, 0, len(backup.Profiles))
	for _, item := range backup.Profiles {
		names = append(names, item.Profile.Name)
	}
	sort.Strings(names)
	return names
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
