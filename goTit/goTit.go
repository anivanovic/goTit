package main

import (
	"encoding/binary"

	"net"
	"time"

	"os"

	"sync"

	"strconv"

	"os/signal"
	"syscall"

	"io/ioutil"

	"flag"

	"os/user"

	"github.com/anivanovic/goTit/metainfo"
	log "github.com/sirupsen/logrus"
)

var (
	listenPort     *uint
	downloadFolder *string
	torrentPath    *string
	logLevel       *string
	peerNum        *int
)

// set up logger
func init() {
	user, _ := user.Current()
	defaultDownloadFolder := user.HomeDir + string(os.PathSeparator) + "Downloads"
	if _, err := os.Stat(defaultDownloadFolder); os.IsNotExist(err) {
		defaultDownloadFolder = user.HomeDir
	}

	torrentPath = flag.String("file", "", "Path to torrent file")
	downloadFolder = flag.String("out", defaultDownloadFolder, "Path to download location")
	listenPort = flag.Uint("port", 8999, "Port used for listening incoming peer requests")
	logLevel = flag.String("log-level", "fatal", "Log level for printing messages to console")
	peerNum = flag.Int("peer-num", 500, "Number of concurrent peer download connections")

	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		level = log.FatalLevel
	}
	log.SetOutput(os.Stdout)
	log.SetLevel(level)
}

func CheckError(err error) {
	if err != nil {
		log.Warnf("%T %+v", err, err)
	}
}

func readConn(conn net.Conn) []byte {
	response := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)

	for {
		conn.SetDeadline(time.Now().Add(timeout))
		n, err := conn.Read(tmp)
		if err != nil {
			CheckError(err)
			break
		}
		log.WithField("read data", n).Info("Read data from connection")
		response = append(response, tmp[:n]...)
	}

	return response
}

func readHandshake(conn net.Conn) []byte {
	response := make([]byte, 0, 68)

	conn.SetDeadline(time.Now().Add(timeout))
	n, err := conn.Read(response)
	if err != nil {
		CheckError(err)
		return response
	}
	log.WithField("read data", n).Info("Read data from peer handshake")

	return response
}

func main() {
	flag.Parse()
	if *torrentPath == "" {
		flag.PrintDefaults()
		os.Exit(2)
	}

	torrentContent, _ := ioutil.ReadFile(*torrentPath)
	torrentString := string(torrentContent)
	_, benDict := metainfo.Parse(torrentString)
	log.Info("Parsed torrent file")
	log.Debug(benDict.String())

	torrent := NewTorrent(*benDict)

	announceList := torrent.Announce_list
	announceList = append(announceList, torrent.Announce)
	ips := make(map[string]bool)

	waitCh := make(chan string, 2)
	lock := sync.Mutex{}
	for i, trackerUrl := range announceList {
		go func(url string) {
			tracker_ips := announceToTracker(url, torrent)
			if tracker_ips != nil {
				for k, v := range *tracker_ips {
					lock.Lock()
					ips[k] = v
					lock.Unlock()
				}
			}
			if i == len(announceList)-1 {
				time.Sleep(time.Second * 5)
				waitCh <- strconv.Itoa(len(announceList) - 1)
			}
		}(trackerUrl)
	}

	val := <-waitCh
	log.Info("Got first " + val)

	log.WithField("size", len(ips)).Info("Peers in pool")
	createTorrentFiles(torrent)

	mng := NewMng(torrent)
	pieceCh := make(chan *peerMessage, 1000)
	go writePiece(pieceCh, torrent)

	indx := 0
	for ip, _ := range ips {
		if indx < *peerNum {
			go func(ip string, torrent *Torrent) {
				peer := NewPeer(ip, torrent, mng, pieceCh)
				err := peer.Announce()
				CheckError(err)
				if err == nil {
					peer.GoMessaging()
				}
			}(ip, torrent)
			indx++
		}
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	signal := <-sigs
	log.Info("got second " + signal.String())
}

func createTorrentFiles(torrent *Torrent) {

	torrentDirPath := *downloadFolder + torrent.Name
	if torrent.IsDirectory {
		err := os.Mkdir(torrentDirPath, os.ModeDir)
		CheckError(err)
		for _, torrentFile := range torrent.TorrentFiles {
			file, err := os.Create(torrentDirPath + "/" + torrentFile.Path)
			CheckError(err)
			torrent.OsFiles = append(torrent.OsFiles, file)
		}
	} else {
		torrentFile, err := os.Create(torrentDirPath)
		CheckError(err)
		torrent.OsFiles = append(torrent.OsFiles, torrentFile)
	}
}

func writePiece(pieceCh <-chan *peerMessage, torrent *Torrent) {
	for true {
		pieceMsg := <-pieceCh
		indx := binary.BigEndian.Uint32(pieceMsg.payload[:4])
		offset := binary.BigEndian.Uint32(pieceMsg.payload[4:8])
		log.WithFields(log.Fields{
			"index":  indx,
			"offset": offset,
		}).Debug("Received piece message for writing to file")
		piecePoss := (int(indx)*torrent.PieceLength + int(offset))

		if torrent.IsDirectory {
			torFiles := torrent.TorrentFiles
			for indx, torFile := range torFiles {
				if torFile.Length < piecePoss {
					piecePoss = piecePoss - torFile.Length
					continue
				} else {
					log.WithFields(log.Fields{
						"file":      torFile.Path,
						"possition": piecePoss,
					}).Debug("Writting to file ")

					log.WithField("size", pieceMsg.size).Debug("Piece msg for writing")
					pieceLen := len(pieceMsg.payload[8:])
					unoccupiedLength := torFile.Length - piecePoss
					file := torrent.OsFiles[indx]
					if unoccupiedLength > pieceLen {
						file.WriteAt(pieceMsg.payload[8:], int64(piecePoss))
					} else {
						file.WriteAt(pieceMsg.payload[8:8+unoccupiedLength], int64(piecePoss))
						piecePoss += unoccupiedLength
						file = torrent.OsFiles[indx+1]

						log.WithFields(log.Fields{
							"file":      file.Name(),
							"possition": piecePoss,
						}).Debug("Writting to file ")
						file.WriteAt(pieceMsg.payload[8+unoccupiedLength:], 0)
					}
					file.Sync()
					break
				}
			}
		} else {
			files := torrent.OsFiles
			file := files[0]
			file.WriteAt(pieceMsg.payload[8:], int64(piecePoss))
			file.Sync()
		}
	}
}

func announceToTracker(url string, torrent *Torrent) *map[string]bool {
	tracker, err := CreateTracker(url)
	if err != nil {
		CheckError(err)
		return nil
	}

	if tracker != nil {
		defer tracker.Close()
		ips, err := tracker.Announce(torrent)
		if err != nil {
			CheckError(err)
			return nil
		}

		return ips
	}

	return nil
}
