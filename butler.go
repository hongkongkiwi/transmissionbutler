package main

import (
	"sync"
	"time"

	"github.com/hekmon/transmissionrpc"
)

func butler(conf *butlerConfig, stopSignal <-chan struct{}, wg *sync.WaitGroup) {
	logger.Infof("[Butler] Will work every %v", conf.CheckFrequency)
	defer wg.Done()
	// Create the ticker
	tick := time.NewTicker(conf.CheckFrequency)
	defer tick.Stop()
	// Start first batch
	butlerBatch(conf)
	// Wait for ticks or cancellation
	for {
		select {
		case <-tick.C:
			butlerBatch(conf)
		case <-stopSignal:
			logger.Debug("[Butler] stop signal received")
			return
		}
	}
}

var fields = []string{"id", "name", "status", "doneDate", "isFinished", "seedRatioLimit", "seedRatioMode", "uploadRatio"}

const (
	seedRatioModeGlobal  = int64(0)
	seedRatioModeCustom  = int64(1)
	seedRatioModeNoRatio = int64(2)
)

func butlerBatch(conf *butlerConfig) {
	// Check that global ratio limit is activated and set with correct value
	//// TODO
	// Get all torrents status
	logger.Debug("[Butler] Fetching torrents' data")
	torrents, err := transmission.TorrentGet(fields, nil)
	if err != nil {
		logger.Errorf("[Butler] Can't retreive torrent(s): %v", err)
		return
	}
	logger.Infof("[Butler] Fetched %d torrent(s) metadata", len(torrents))
	// Inspect each torrent
	now := time.Now()
	youngTorrents := make([]int64, 0, len(torrents))
	regularTorrents := make([]int64, 0, len(torrents))
	finishedTorrents := make([]int64, 0, len(torrents))
	for index, torrent := range torrents {
		// Checks
		if !torrentOK(torrent, index) {
			continue
		}
		// We can now safely access metadata
		logger.Debugf("[Butler] Inspecting torrent %d:\n\tid: %d\n\tname: %s\n\tstatus: %d\n\tdoneDate: %v\n\tisFinished: %v\n\tseedRatioLimit: %f\n\tseedRatioMode: %d\n\tuploadRatio:%f",
			index, *torrent.ID, *torrent.Name, *torrent.Status, *torrent.DoneDate, *torrent.IsFinished, *torrent.SeedRatioLimit, *torrent.SeedRatioMode, *torrent.UploadRatio)
		// Is this a custom torrent, should we left it alone ?
		if *torrent.SeedRatioMode == seedRatioModeCustom {
			logger.Infof("[Butler] Torent id %d (%s) has a custom ratio limit: considering it as custom (skipping)", *torrent.ID, *torrent.Name)
			continue
		}
		// Does this torrent is under/over the free seed time range ?
		if torrent.DoneDate.Add(conf.UnlimitedSeed).After(now) {
			// Torrent is still within the free seed time
			if *torrent.SeedRatioMode != seedRatioModeNoRatio {
				logger.Infof("[Butler] Torent id %d (%s) is still young: adding it to the unlimited seed ratio list", *torrent.ID, *torrent.Name)
				youngTorrents = append(youngTorrents, *torrent.ID)
			}
		} else {
			// Torrent is over the free seed time
			if *torrent.SeedRatioMode != seedRatioModeGlobal {
				logger.Infof("[Butler] Torent id %d (%s) has now ended it's unlimited seed time: adding it to the regular ratio list", *torrent.ID, *torrent.Name)
				regularTorrents = append(regularTorrents, *torrent.ID)
			}
		}
		// Does this torrent is finished ?
		if *torrent.IsFinished {
			if conf.DeleteDone {
				logger.Infof("[Butler] Torent id %d (%s) is finished (ratio %f): adding it to deletion list", *torrent.ID, *torrent.Name, *torrent.UploadRatio)
				finishedTorrents = append(finishedTorrents, *torrent.ID)
			} else {
				logger.Infof("[Bulter] Torent id %d (%s) is finished (ratio %f) but auto deletion is disable: skipping", *torrent.ID, *torrent.Name, *torrent.UploadRatio)
			}
		}
	}
	// Switch to unlimited seed young torrents
	if len(youngTorrents) > 0 {
		seedRatioMode := seedRatioModeNoRatio
		err = transmission.TorrentSet(&transmissionrpc.TorrentSetPayload{
			IDs:           youngTorrents,
			SeedRatioMode: &seedRatioMode,
		})
		if err != nil {
			logger.Errorf("[Butler] Can't apply no ratio mutator to the %d young torrent(s): %v", len(youngTorrents), err)
		} else {
			logger.Infof("[Butler] Successfully applied the no ratio mutator to the %d young torrent(s)", len(youngTorrents))
		}
	}
	// Switch to global ratio mode regular torrents
	if len(regularTorrents) > 0 {
		seedRatioMode := seedRatioModeGlobal
		err = transmission.TorrentSet(&transmissionrpc.TorrentSetPayload{
			IDs:           regularTorrents,
			SeedRatioMode: &seedRatioMode,
		})
		if err != nil {
			logger.Errorf("[Butler] Can't apply global ratio mutator to the %d regular torrent(s): %v", len(regularTorrents), err)
		} else {
			logger.Infof("[Butler] Successfully applied the global ratio mutator to the %d regular torrent(s)", len(regularTorrents))
		}
	}
	// Delete finished torrents
	if len(finishedTorrents) > 0 {
		err = transmission.TorrentDelete(&transmissionrpc.TorrentDeletePayload{
			IDs:             finishedTorrents,
			DeleteLocalData: true,
		})
		if err != nil {
			logger.Errorf("[Butler] Can't delete the %d finished torrent(s): %v", len(finishedTorrents), err)
		} else {
			logger.Infof("[Butler] Successfully deleted the %d finished torrent(s)", len(finishedTorrents))
		}
	}
}

func torrentOK(torrent *transmissionrpc.Torrent, index int) (ok bool) {
	if torrent == nil {
		logger.Warningf("[Butler] Encountered a nil torrent at index %d", index)
		return
	}
	if torrent.ID == nil {
		logger.Warningf("[Butler] Encountered a nil torrent id at index %d", index)
		return
	}
	if torrent.Name == nil {
		logger.Warningf("[Butler] Encountered a nil torrent name at index %d", index)
		return
	}
	if torrent.Status == nil {
		logger.Warningf("[Butler] Encountered a nil torrent status at index %d", index)
		return
	}
	if torrent.DoneDate == nil {
		logger.Warningf("[Butler] Encountered a nil torrent doneDate at index %d", index)
		return
	}
	if torrent.IsFinished == nil {
		logger.Warningf("[Butler] Encountered a nil torrent isFinished at index %d", index)
		return
	}
	if torrent.SeedRatioLimit == nil {
		logger.Warningf("[Butler] Encountered a nil torrent seedRatioLimit at index %d", index)
		return
	}
	if torrent.SeedRatioMode == nil {
		logger.Warningf("[Butler] Encountered a nil torrent seedRatioMode at index %d", index)
		return
	}
	if torrent.UploadRatio == nil {
		logger.Warningf("[Butler] Encountered a nil torrent ID at index %d", index)
		return
	}
	return true
}