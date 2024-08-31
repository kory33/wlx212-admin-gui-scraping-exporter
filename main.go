package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

func retryImmediately[T any](f func() (*T, error), maxRetryCount int) (*T, /* last error if we had to give up */ error, /* all encountered errors */ []error) {
  // require maxRetryCount to be at least 1
	if maxRetryCount < 1 {
		panic("maxRetryCount must be at least 1")
	}

	var errs []error
	for i := 0; i < maxRetryCount; i++ {
		if result, err := f(); err != nil {
			errs = append(errs, err)
		} else {
			return result, nil, errs
		}
	}

	return nil, errs[len(errs)-1], errs
}

func getHtmlWithBasicAuth(url string, user string, pass string) (*html.Node, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(user, pass)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return html.Parse(strings.NewReader(string(bytes)))
}

func htmlNodeChildren(node *html.Node) []*html.Node {
	children := []*html.Node{}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		children = append(children, child)
	}
	return children
}

func findFirstHtmlNodeIncludingSelfSatisfyingPredicate(n *html.Node, predicate func(*html.Node) bool) *html.Node {
	if predicate(n) {
		return n
	}

	for _, child := range htmlNodeChildren(n) {
		if nodeInChild := findFirstHtmlNodeIncludingSelfSatisfyingPredicate(child, predicate); nodeInChild != nil {
			return nodeInChild
		}
	}

	return nil
}

func findFirstHtmlNodeWithIdIn(n *html.Node, id string) *html.Node {
	return findFirstHtmlNodeIncludingSelfSatisfyingPredicate(n, func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			for _, attr := range n.Attr {
				if attr.Key == "id" && attr.Val == id {
					return true
				}
			}
		}
		return false
	})
}

type EnvVars struct {
	VirtualControllerVIP     string
	VirtualControllerGUIUser string
	VirtualControllerGUIPass string
}

type AccessPointReadFromControllerGUI struct {
	HostName          string `json:"hostname"`
	IpAddress         string `json:"ip_address"`
}

type AccessPointDetailReadFromTargetApGUI struct {
	Active2_4GHzConnections int `json:"active_2_4ghz_connections"`
	Active5GHzConnections int `json:"active_5ghz_connections"`
}

type ReconstructedApData struct {
	AccessPointReadFromControllerGUI
	AccessPointDetailReadFromTargetApGUI
}

func findScriptContainingApListData(topNode *html.Node) *string {
	node := findFirstHtmlNodeIncludingSelfSatisfyingPredicate(topNode, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "script" && n.FirstChild != nil && strings.Contains(n.FirstChild.Data, "var apListData=[")
	})

	if node != nil {
		return &node.FirstChild.Data
	} else {
		return nil
	}
}

var extractNumber = regexp.MustCompile("[0-9]+")
var lastElementTrailingComma = regexp.MustCompile(`,\s*]`)

func extractApListDataFromScriptText(script string) ([]AccessPointReadFromControllerGUI, error) {
	var data [][]interface{}
	dataString := lastElementTrailingComma.ReplaceAll(
		[]byte(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(script), "var apListData="), ";")),
		// replace last element's trailing comma, as in [..., ...,] -> [..., ...]
		[]byte("]"),
	)

	if err := json.Unmarshal([]byte(dataString), &data); err != nil {
		return nil, err
	}

	aps := make([]AccessPointReadFromControllerGUI, len(data))
	for i, apData := range data {
		aps[i] = AccessPointReadFromControllerGUI{
			HostName:          apData[7].(string),
			IpAddress:         apData[13].(string),
		}
	}

	return aps, nil
}

func fetchAllAccessPointsFromController(env EnvVars) ([]AccessPointReadFromControllerGUI, error) {
	topHtmlNode, err := getHtmlWithBasicAuth(
		fmt.Sprintf("http://%s/top-virtual-controller.html", env.VirtualControllerVIP),
		env.VirtualControllerGUIUser,
		env.VirtualControllerGUIPass,
	)
	if err != nil {
		return nil, err
	}

	// search for a script tag containing "var apListData = [...];"
	script := findScriptContainingApListData(topHtmlNode)
	if script == nil {
		return nil, fmt.Errorf("could not find script node with apListData")
	}

	return extractApListDataFromScriptText(*script)
}

func fetchApDetailFromApGUI(env EnvVars, ap AccessPointReadFromControllerGUI) (*AccessPointDetailReadFromTargetApGUI, error) {
	topHtmlNode, err := getHtmlWithBasicAuth(
		fmt.Sprintf("http://%s/manage-system.html", ap.IpAddress),
		env.VirtualControllerGUIUser,
		env.VirtualControllerGUIPass,
	)
	if err != nil {
		return nil, err
	}

	active2_4GhzConnections, err := func() (int, error) {
		tableRow := findFirstHtmlNodeWithIdIn(topHtmlNode, "2G_connect_count_form")
		if tableRow == nil {
			return 0, fmt.Errorf("no node with id=2G_connect_count_form")
		}
		countDataNode := htmlNodeChildren(tableRow)
		if len(countDataNode) < 4 || countDataNode[3].FirstChild == nil {
			return 0, fmt.Errorf("child of node at index 4 expected")
		}

		return strconv.Atoi(extractNumber.FindString(countDataNode[3].FirstChild.Data))
	}();
	if err != nil {
		return nil, fmt.Errorf("failed to find 2GHz connection count: %w", err)
	}

	active5GhzConnections, err := func() (int, error) {
		tableRow := findFirstHtmlNodeWithIdIn(topHtmlNode, "5G1_connect_count_form")
		if tableRow == nil {
			return 0, fmt.Errorf("no node with id=5G1_connect_count_form")
		}
		countDataNode := htmlNodeChildren(tableRow)
		if len(countDataNode) < 4 || countDataNode[3].FirstChild == nil {
			return 0, fmt.Errorf("child of node at index 4 expected")
		}

		return strconv.Atoi(extractNumber.FindString(countDataNode[3].FirstChild.Data))
	}();
	if err != nil {
		return nil, fmt.Errorf("failed to find 5GHz connection count: %w", err)
	}

	return &AccessPointDetailReadFromTargetApGUI{
		Active2_4GHzConnections: active2_4GhzConnections,
		Active5GHzConnections: active5GhzConnections,
	}, nil
}

func reconstructAllApData(env EnvVars) ([]ReconstructedApData, error) {
	aps, err, allErrs := retryImmediately(
		func() (*[]AccessPointReadFromControllerGUI, error) {
			aps, err := fetchAllAccessPointsFromController(env)
			return &aps, err
		},
		3,
	)
	if err != nil {
		return nil, err
	}
	if len(allErrs) > 0 {
		slog.Info(fmt.Sprintf("retried fetching AP info from controller %d times, last error: %s", len(allErrs), allErrs[len(allErrs)-1].Error()))
	}

	// fan-out fetching details and then join all.
	// This process may fail, in which case nil must be communicated.
	detailChan := make(chan *AccessPointDetailReadFromTargetApGUI)
	for _, ap := range *aps {
		go func() {
			detail, err, allErrs := retryImmediately(
				func() (*AccessPointDetailReadFromTargetApGUI, error) { return fetchApDetailFromApGUI(env, ap) },
				5,
			)
			if err != nil {
				slog.Warn(fmt.Sprintf("error fetching detail for %s: error after %d retries: %v", ap.HostName, len(allErrs), err))
				detailChan <- nil
				return
			}
			if len(allErrs) > 0 {
				slog.Info(fmt.Sprintf("retried fetching detail for %s %d times, last error: %v", ap.HostName, len(allErrs), allErrs[len(allErrs)-1]))
			}
			detailChan <- detail
		}()
	}

	reconstructedAps := []ReconstructedApData{}
	for _, ap := range *aps {
		detail := <-detailChan
		if detail == nil {
			slog.Warn(fmt.Sprintf("No details obtained for %s", ap.HostName))
			continue
		}

		reconstructedAps = append(reconstructedAps, ReconstructedApData{
			AccessPointReadFromControllerGUI: ap,
			AccessPointDetailReadFromTargetApGUI: *detail,
		})
	}

	return reconstructedAps, nil
}

// return fetchAllAccessPoints as a JSON response
func aplist(env EnvVars, w http.ResponseWriter, _ *http.Request) {
	// fetch all access points
	aps, err := reconstructAllApData(env)
	if err != nil {
		slog.Warn(fmt.Sprintf("error fetching access points: %v", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// write the response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(aps); err != nil {
		slog.Warn(fmt.Sprintf("error encoding access points: %v", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func metrics(env EnvVars, w http.ResponseWriter, _ *http.Request) {
	// fetch all access points
	aps, err := reconstructAllApData(env)
	if err != nil {
		slog.Warn(fmt.Sprintf("error fetching access points: %v", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	appendLineToResponse := func(line string) error {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			slog.Error(fmt.Sprintf("error writing access points: %v", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}
		return nil
	}

	// write the response
	w.Header().Set("Content-Type", "text/plain")
	for _, ap := range aps {
		if err = appendLineToResponse(fmt.Sprintf("ap_active_connections{hostname=\"%s\",frequency=\"2.4GHz\"} %d", ap.HostName, ap.Active2_4GHzConnections)); err != nil {
			return
		}
		if err = appendLineToResponse(fmt.Sprintf("ap_active_connections{hostname=\"%s\",frequency=\"5GHz\"} %d", ap.HostName, ap.Active5GHzConnections)); err != nil {
			return
		}
	}
}

func requireNonEmptyEnv(key string) string {
	envVar := os.Getenv(key)
	if envVar == "" {
		slog.Error(fmt.Sprintf("%s is required", key))
		os.Exit(1)
	}
	return envVar
}

func main() {
	slog.Info("Reading environment variables...")

	var serverPort int
	if port, err := strconv.Atoi(os.Getenv("PORT")); err == nil {
		serverPort = port
	} else {
		serverPort = 8080
	}

	env := EnvVars{
		VirtualControllerVIP:     requireNonEmptyEnv("VIRTUAL_CONTROLLER_VIP"),
		VirtualControllerGUIUser: requireNonEmptyEnv("VIRTUAL_CONTROLLER_GUI_USER"),
		VirtualControllerGUIPass: requireNonEmptyEnv("VIRTUAL_CONTROLLER_GUI_PASS"),
	}

	http.HandleFunc("/aplist", func(w http.ResponseWriter, r *http.Request) {
		aplist(env, w, r)
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metrics(env, w, r)
	})

	slog.Info("Starting server...", "port", serverPort)
	if err := http.ListenAndServe(":"+strconv.Itoa(serverPort), nil); err != nil {
		slog.Error("error starting server", "error", err.Error())
	}
}
