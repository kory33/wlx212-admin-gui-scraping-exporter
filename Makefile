.PHONY: init
init:
ifeq ($(shell uname -s),Darwin)
	@grep -r -l wlx212-admin-gui-scraping-exporter * .goreleaser.yaml | xargs sed -i "" "s/wlx212-admin-gui-scraping-exporter/$$(basename `git rev-parse --show-toplevel`)/"
else
	@grep -r -l wlx212-admin-gui-scraping-exporter * .goreleaser.yaml | xargs sed -i "s/wlx212-admin-gui-scraping-exporter/$$(basename `git rev-parse --show-toplevel`)/"
endif
