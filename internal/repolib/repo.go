package repolib

import (
	"archive/tar"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"github.intel.com/crlynch/clr-dissector/internal/downloader"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	_ "github.com/mutecomm/go-sqlcipher"
	"github.com/ulikunitz/xz"
	"github.com/sassoftware/go-rpmutils"
)

type Repomd struct {
	XMLName xml.Name `xml:"repomd"`
	Data    []Data   `xml:"data"`
}

type Data struct {
	XMLName      xml.Name     `xml:"data"`
	Type         string       `xml:"type,attr"`
	Location     Location     `xml:"location"`
	Checksum     Checksum     `xml:"checksum"`
	OpenChecksum OpenChecksum `xml:"open-checksum"`
}

type Location struct {
	XMLName xml.Name `xml:"location"`
	Href    string   `xml:"href,attr"`
}

type Checksum struct {
	XMLName xml.Name `xml:"checksum"`
	Type    string   `xml:"type,attr"`
	Value   string   `xml:",chardata"`
}

type OpenChecksum struct {
	XMLName xml.Name `xml:"open-checksum"`
	Type    string   `xml:"type,attr"`
	Value   string   `xml:",chardata"`
}

func DownloadRepo(version int, url string) error {
	db := fmt.Sprintf("%d/repodata/primary.sqlite", version)
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		// Already downloaded
		return nil
	}

	config_url := fmt.Sprintf(
		"%s/releases/%d/clear/x86_64/os/repodata/repomd.xml",
		url, version)

	resp, err := http.Get(config_url)
	if err != nil {
		return err

	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.Status != "200 OK" {
		return errors.New(fmt.Sprintf("Unable to find release %d on %s",
			version, url))
	}

	err = os.MkdirAll(fmt.Sprintf("%d/repodata", version), 0700)
	if err != nil {
		return err
	}

	err = os.MkdirAll(fmt.Sprintf("%d/source", version), 0700)
	if err != nil {
		return err
	}

	err = os.MkdirAll(fmt.Sprintf("%d/srpms", version), 0700)
	if err != nil {
		return err
	}

	var repomd Repomd
	xml.Unmarshal(body, &repomd)
	for i := 0; i < len(repomd.Data); i++ {
		href := repomd.Data[i].Location.Href
		cs := repomd.Data[i].Checksum.Value
		url := fmt.Sprintf(
			"%s/releases/%d/clear/x86_64/os/%s",
			url, version, href)

		if strings.HasSuffix(href, "primary.sqlite.xz") {
			err := downloader.DownloadFile(db+".xz", url, cs)
			if err != nil {
				return err
			}

			fmt.Printf("Uncompressing %s -> %s\n", db+".xz", db)

			f, err := os.Open(db + ".xz")
			if err != nil {
				return err
			}
			defer f.Close()

			r, err := xz.NewReader(f)
			if err != nil {
				return err
			}

			w, err := os.Create(db)
			if err != nil {
				return err
			}
			defer w.Close()

			if _, err = io.Copy(w, r); err != nil {
				return err
			}

			err = os.Remove(db + ".xz")
			if err != nil {
				return nil
			}
		}
	}

	return nil
}

func GetPkgMap(version int) (map[string]string, error) {
	pmap := make(map[string]string)
	db, err := sql.Open("sqlite3", fmt.Sprintf("%d/repodata/primary.sqlite",
		version))
	if err != nil {
		return pmap, err
	}
	defer db.Close()

	rows, err := db.Query("select name, rpm_sourcerpm from packages;")
	if err != nil {
		return pmap, err
	}
	defer rows.Close()

	for rows.Next() {
		var name, srpm string
		err := rows.Scan(&name, &srpm)
		if err != nil {
			return pmap, nil
		}
		pmap[name] = srpm
	}

	return pmap, nil
}

func DownloadBundles(clear_version int) error {
	bundle_path := fmt.Sprintf("%d/bundles", clear_version)
	if _, err := os.Stat(bundle_path + "/.complete"); !os.IsNotExist(err) {
		// Already downloaded
		return nil
	}

	err := os.MkdirAll(bundle_path, 0700)
	if err != nil {
		return err
	}

	config_url := fmt.Sprintf("https://cdn.download.clearlinux.org/"+
		"packs/%d/pack-os-core-update-index-from-0.tar",
		clear_version)

	resp, err := http.Get(config_url)
	if err != nil {
		return err

	}
	defer resp.Body.Close()

	if resp.Status != "200 OK" {
		err := errors.New("Bundle manifest not found on server: " +
			config_url)
		return err
	}

	xzr, err := xz.NewReader(resp.Body)
	if err != nil {
		return err
	}

	tr := tar.NewReader(xzr)

	for {
		header, err := tr.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		if header == nil {
			continue
		}

		content, err := ioutil.ReadAll(tr)
		if err != nil {
			return err
		}

		var config map[string]interface{}
		err = json.Unmarshal(content, &config)
		if err != nil {
			continue
		}

		target := fmt.Sprintf("%d/bundles/%s", clear_version, config["Name"])
		err = ioutil.WriteFile(target, content, 0644)
		if err != nil {
			return err
		}
	}

	f, _ := os.Create(bundle_path + "/.complete")
	f.Close()

	return nil
}

func GetBundle(clear_version int, name string) (map[string]interface{}, error) {
	var bundle map[string]interface{}

	err := DownloadBundles(clear_version)
	if err != nil {
		return bundle, err
	}

	f, err := os.Open(fmt.Sprintf("%d/bundles/%s", clear_version, name))
	if err != nil {
		return bundle, err
	}
	defer f.Close()

	content, err := ioutil.ReadAll(f)
	if err != nil {
		return bundle, err
	}

	err = json.Unmarshal(content, &bundle)
	if err != nil {
		return bundle, errors.New("Corrupt bundle content")
	}

	return bundle, nil
}

func ExtractRpm(archive string, target string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	rpm, err := rpmutils.ReadRpm(f)
	if err != nil {
		return err
	}

	err = rpm.ExpandPayload(target)
	if err != nil {
		return err
	}
	return nil
}
