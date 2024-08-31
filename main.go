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

func retryImmediately[T any](f func() (*T, error), maxRetryCount int) (*T, error, /* retried = */ int) {
	var err error
	for i := 0; i < maxRetryCount; i++ {
		result, err := f()
		if err == nil {
			return result, nil, i
		}
	}
	return nil, err, maxRetryCount
}

type EnvVars struct {
	VirtualControllerVIP     string
	VirtualControllerGUIUser string
	VirtualControllerGUIPass string
}

type AccessPointReadFromControllerGUI struct {
	HostName          string `json:"hostname"`
	ActiveConnections int    `json:"active_connections"`
	IpAddress         string `json:"ip_address"`
}

type AccessPointDetailReadFromTargetApGUI struct {
	Active5GHzConnections int `json:"active_5ghz_connections"`
	Active2_4GHzConnections int `json:"active_2_4ghz_connections"`
}

type ReconstructedApData struct {
	AccessPointReadFromControllerGUI
	AccessPointDetailReadFromTargetApGUI
}

func fetchControllerGUIHtml(env EnvVars) (string, error) {
	// fetch http://<VirtualControllerVIP>/top-virtual-controller.html with basic auth
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/top-virtual-controller.html", env.VirtualControllerVIP), nil)
	if err != nil {
		return "", err
	}

	req.SetBasicAuth(env.VirtualControllerGUIUser, env.VirtualControllerGUIPass)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

func findScriptContainingApListData(n *html.Node) *string {
	if n.Type == html.ElementNode && n.Data == "script" && n.FirstChild != nil {
		if strings.Contains(n.FirstChild.Data, "var apListData=[") {
			return &n.FirstChild.Data
		}
	}

	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if nodeInChild := findScriptContainingApListData(child); nodeInChild != nil {
			return nodeInChild
		}
	}

	return nil
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

	err := json.Unmarshal([]byte(dataString), &data)
	if err != nil {
		return nil, err
	}

	aps := make([]AccessPointReadFromControllerGUI, len(data))
	for i, apData := range data {
		hostName := apData[7].(string)
		// apData[10] is suppsed to be a string like "9/100" or "10/100", so parse the first number
		activeConnections, err := strconv.Atoi(extractNumber.FindString(apData[10].(string)))
		if err != nil {
			return nil, err
		}

		aps[i] = AccessPointReadFromControllerGUI{
			HostName:          hostName,
			ActiveConnections: activeConnections,
			IpAddress:         apData[13].(string),
		}
	}

	return aps, nil
}

func fetchAllAccessPointsFromController(env EnvVars) ([]AccessPointReadFromControllerGUI, error) {
	gui, err := fetchControllerGUIHtml(env)
	if err != nil {
		return nil, err
	}

	// search for a script tag containing "var apListData = [...];"
	topNode, err := html.Parse(strings.NewReader(gui))
	if err != nil {
		return nil, err
	}

	script := findScriptContainingApListData(topNode)
	if script == nil {
		return nil, fmt.Errorf("could not find script node with apListData")
	}

	return extractApListDataFromScriptText(*script)
}

func fetchApDetailFromApGUI(env EnvVars, ap AccessPointReadFromControllerGUI) (AccessPointDetailReadFromTargetApGUI, error) {
	return AccessPointDetailReadFromTargetApGUI{}, nil
}

func reconstructAllApData(env EnvVars) ([]ReconstructedApData, error) {
	aps, err, retried := retryImmediately(
		func() (*[]AccessPointReadFromControllerGUI, error) {
			aps, err := fetchAllAccessPointsFromController(env)
			return &aps, err
		},
		3,
	)
	if err != nil {
		return nil, err
	}
	if retried > 0 {
		slog.Info(fmt.Sprintf("retried fetching AP info from controller %d times", retried))
	}

	detailChan := make(chan AccessPointDetailReadFromTargetApGUI)
	for _, ap := range *aps {
		go func() {
			detail, err, retried := retryImmediately(
				func() (*AccessPointDetailReadFromTargetApGUI, error) {
					detail, err := fetchApDetailFromApGUI(env, ap)
					return &detail, err
				},
				5,
			)
			if err != nil {
				slog.Warn(fmt.Sprintf("error fetching detail for %s: after %d retries %v", ap.HostName, retried, err))
				return
			}
			if retried > 0 {
				slog.Info(fmt.Sprintf("retried fetching detail for %s %d times", ap.HostName, retried))
			}
			detailChan <- *detail
		}()
	}

	reconstructedAps := make([]ReconstructedApData, len(*aps))
	for i, ap := range *aps {
		reconstructedAps[i] = ReconstructedApData{
			AccessPointReadFromControllerGUI: ap,
			AccessPointDetailReadFromTargetApGUI: <-detailChan,
		}
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

	// write the response
	w.Header().Set("Content-Type", "text/plain")
	for _, ap := range aps {
		_, err = w.Write([]byte(fmt.Sprintf("ap_active_connections{hostname=\"%s\"} %d\n", ap.HostName, ap.ActiveConnections)))
		if err != nil {
			slog.Error(fmt.Sprintf("error writing access points: %v", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
