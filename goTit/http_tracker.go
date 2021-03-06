package main

import (
	"net/http"
	"net/url"

	"strconv"

	"io/ioutil"

	"errors"

	"encoding/binary"
	"net"

	"github.com/anivanovic/goTit/metainfo"
	log "github.com/sirupsen/logrus"
)

type http_tracker struct {
	Url              *url.URL
	AnnounceInterval int
	Ips              *map[string]bool
}

func httpTracker(url *url.URL) Tracker {
	t := new(http_tracker)
	t.Url = url

	return t
}

func (t *http_tracker) Announce(torrent *Torrent) (*map[string]bool, error) {
	query := t.Url.Query()
	query.Set("info_hash", string(torrent.Hash))
	query.Set("peer_id", string(torrent.PeerId))
	query.Set("port", strconv.Itoa(int(*listenPort)))
	query.Set("uploaded", "0")
	query.Set("downloaded", "0")
	query.Set("left", "0")
	query.Set("compact", "1")
	t.Url.RawQuery = query.Encode()

	res, err := http.Get(t.Url.String())

	if err != nil {
		CheckError(err)
		return nil, err
	}

	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)

	body := string(data)
	logger := log.WithFields(log.Fields{
		"statusCode": res.StatusCode,
		"body":       body,
		"url":        t.Url.String(),
	})
	if res.StatusCode != 200 {
		logger.Warn("Invalid request to http tracker")
		return nil, errors.New("http tracker returned response code " + strconv.Itoa(res.StatusCode))
	}

	logger.WithField("body", body).Debug("Http tracker announce response")
	_, dict := metainfo.Decode(body)
	ips := readHttpAnnounce(dict)
	return ips, nil
}

func (t *http_tracker) Close() error { return nil }

func readHttpAnnounce(elem metainfo.Bencode) *map[string]bool {
	if benDict, ok := elem.(metainfo.DictElement); ok {
		peers := benDict.Value("peers").String()
		ipData := []byte(peers)
		size := len(ipData)
		peerCount := size / 6
		ips := make(map[string]bool, 0)
		for read := 0; read < peerCount; read++ {
			byteMask := 6

			ipAddress := net.IPv4(ipData[byteMask*read], ipData[byteMask*read+1], ipData[byteMask*read+2], ipData[byteMask*read+3])
			port := binary.BigEndian.Uint16(ipData[byteMask*read+4 : byteMask*read+6])
			ipAddr := ipAddress.String() + ":" + strconv.Itoa(int(port))
			ips[ipAddr] = true
		}
		return &ips
	}
	return nil
}
