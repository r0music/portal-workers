package workers

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/inc-backend/go-incognito/publish/transformer"
	"github.com/incognitochain/portal-workers/entities"
	"github.com/incognitochain/portal-workers/metadata"
	"github.com/incognitochain/portal-workers/utils"
)

func (b *BTCBroadcastingManager) isTimeoutBTCTx(tx *BroadcastTx, curBlockHeight uint64) bool {
	broadcastBlockHeight := tx.BlkHeight

	if !tx.IsBroadcasted {
		return curBlockHeight-broadcastBlockHeight >= TimeIntervalBTCFeeReplacement
	} else {
		if curBlockHeight-broadcastBlockHeight < TimeoutBTCFeeReplacement {
			return false
		}

		txHash, _ := chainhash.NewHashFromStr(tx.TxHash)
		tx, err := b.btcClient.GetRawTransactionVerbose(txHash)
		return err != nil || tx.Confirmations <= 0
	}
}

// return boolean value of transaction confirmation and bitcoin block height
func (b *BTCBroadcastingManager) isConfirmedBTCTx(txHash string) (bool, uint64) {
	txID, _ := chainhash.NewHashFromStr(txHash)
	tx, err := b.btcClient.GetRawTransactionVerbose(txID)
	if err != nil {
		return false, 0
	}
	if tx.Confirmations >= BTCConfirmationThreshold {
		blkHash, _ := chainhash.NewHashFromStr(tx.BlockHash)
		blk, err := b.btcClient.GetBlockHeaderVerbose(blkHash)
		if err != nil {
			return false, 0
		}
		return true, uint64(blk.Height)
	}
	return false, 0
}

func (b *BTCBroadcastingManager) getVSizeBTCTx(txContent string) (int, error) {
	// Decode the serialized transaction hex to raw bytes.
	serializedTx, err := hex.DecodeString(txContent)
	if err != nil {
		b.ExportErrorLog(fmt.Sprintf("Could not serialized tx content %v - with err: %v \n", txContent, err))
		return 0, err
	}

	txRawResult, err := b.btcClient.DecodeRawTransaction(serializedTx)
	if err != nil {
		fmt.Printf("Could not decode tx content %v - with err: %v \n", txContent, err)
		// b.ExportErrorLog(fmt.Sprintf("Could not decode tx content %v - with err: %v \n", txContent, err))
		return 0, err
	}

	return int(txRawResult.Vsize), nil
}

func (b *BTCBroadcastingManager) broadcastTx(txContent string) error {
	// Decode the serialized transaction hex to raw bytes.
	serializedTx, err := hex.DecodeString(txContent)
	if err != nil {
		b.ExportErrorLog(fmt.Sprintf("Could not serialized tx content %v - with err: %v \n", txContent, err))
		return err
	}

	msgTx := &wire.MsgTx{}
	err = msgTx.Deserialize(bytes.NewReader(serializedTx))
	if err != nil {
		b.ExportErrorLog(fmt.Sprintf("Could not deserialize tx content %v to MsgTx - with err: %v \n", txContent, err))
		return err
	}

	_, err = b.btcClient.SendRawTransaction(msgTx, true)
	if err != nil {
		b.ExportErrorLog(fmt.Sprintf("Could not broadcast tx content %v to BTC chain - with err: %v \n", txContent, err))
		return err
	}
	return nil
}

// Return fee per unshield request and number of requests
func (b *BTCBroadcastingManager) getUnshieldFeeInfo(batch *entities.ProcessedUnshieldRequestBatch) (uint, uint) {
	minFee := uint(0)
	for _, fee := range batch.ExternalFees {
		if minFee <= 0 || fee < minFee {
			minFee = fee
		}
	}
	return minFee, uint(len(batch.UnshieldsID))
}

func (b *BTCBroadcastingManager) getLatestBeaconHeight() (uint64, error) {
	params := []interface{}{}
	var beaconBestStateRes entities.BeaconBestStateRes
	err := b.RPCClient.RPCCall("getbeaconbeststate", params, &beaconBestStateRes)
	if err != nil {
		return 0, err
	}

	if beaconBestStateRes.RPCError != nil {
		b.Logger.Errorf("getLatestBeaconHeight: call RPC error, %v\n", beaconBestStateRes.RPCError.StackTrace)
		return 0, errors.New(beaconBestStateRes.RPCError.Message)
	}
	return beaconBestStateRes.Result.BeaconHeight, nil
}

func (b *BTCBroadcastingManager) getLatestBTCBlockHashFromIncog() (uint64, error) {
	params := []interface{}{}
	var btcRelayingBestStateRes entities.BTCRelayingBestStateRes
	err := b.RPCClient.RPCCall("getbtcrelayingbeststate", params, &btcRelayingBestStateRes)
	if err != nil {
		return 0, err
	}
	if btcRelayingBestStateRes.RPCError != nil {
		b.Logger.Errorf("getLatestBTCBlockHashFromIncog: call RPC error, %v\n", btcRelayingBestStateRes.RPCError.StackTrace)
		return 0, errors.New(btcRelayingBestStateRes.RPCError.Message)
	}

	// check whether there was a fork happened or not
	btcBestState := btcRelayingBestStateRes.Result
	if btcBestState == nil {
		return 0, errors.New("BTC relaying best state is nil")
	}
	currentBTCBlkHeight := btcBestState.Height
	return uint64(currentBTCBlkHeight), nil
}

func (b *BTCBroadcastingManager) getBroadcastTxsFromBeaconHeight(processedBatchIDs map[string]bool, height uint64, curIncBlkHeight uint64) (map[string]*BroadcastTx, error) {
	txArray := map[string]*BroadcastTx{}
	var params []interface{}

	params = []interface{}{
		map[string]string{
			"BeaconHeight": strconv.FormatUint(height, 10),
		},
	}
	var portalStateRes entities.PortalV4StateByHeightRes
	err := b.RPCClient.RPCCall("getportalv4state", params, &portalStateRes)
	if err != nil {
		return txArray, err
	}
	if portalStateRes.RPCError != nil {
		b.Logger.Errorf("getportalv4state: call RPC error, %v\n", portalStateRes.RPCError.StackTrace)
		return txArray, errors.New(portalStateRes.RPCError.Message)
	}

	for _, batch := range portalStateRes.Result.ProcessedUnshieldRequests[BTCID] {
		_, exists := processedBatchIDs[batch.BatchID]
		if !exists {
			params = []interface{}{
				map[string]string{
					"BatchID": batch.BatchID,
				},
			}
			var signedRawTxRes entities.SignedRawTxRes
			err := b.RPCClient.RPCCall("getportalsignedrawtransaction", params, &signedRawTxRes)
			if err != nil {
				return txArray, err
			}
			if signedRawTxRes.RPCError != nil {
				b.Logger.Errorf("getportalsignedrawtransaction: call RPC for batchID %v - with err %v\n", batch.BatchID, signedRawTxRes.RPCError.StackTrace)
				continue
			}

			btcTxContent := signedRawTxRes.Result.SignedTx
			btcTxHash := signedRawTxRes.Result.TxID
			vsize, err := b.getVSizeBTCTx(btcTxContent)
			if err != nil {
				continue
			}

			feePerRequest, numberRequest := b.getUnshieldFeeInfo(batch)
			acceptableFee := utils.IsEnoughFee(vsize, feePerRequest, numberRequest, b.bitcoinFee)
			txArray[batch.BatchID] = &BroadcastTx{
				TxContent:     btcTxContent,
				TxHash:        btcTxHash,
				VSize:         vsize,
				FeePerRequest: feePerRequest,
				NumOfRequests: numberRequest,
				IsBroadcasted: acceptableFee,
				BlkHeight:     curIncBlkHeight,
			}
		}
	}
	return txArray, nil
}

func (b *BTCBroadcastingManager) getBroadcastReplacementTx(feeReplacementTxArray map[string]*FeeReplacementTx, curIncBlkHeight uint64) (map[string]*FeeReplacementTx, map[string]*BroadcastTx, error) {
	var params []interface{}
	txArray := map[string]*BroadcastTx{}

	for batchID, tx := range feeReplacementTxArray {
		acceptableFee := utils.IsEnoughFee(tx.VSize, tx.FeePerRequest, tx.NumOfRequests, b.bitcoinFee)
		if acceptableFee && tx.ReqTxID != "" {
			params = []interface{}{
				map[string]string{
					"TxID": tx.ReqTxID,
				},
			}
			var signedRawTxRes entities.SignedRawTxRes
			err := b.RPCClient.RPCCall("getportalsignedrawreplacebyfeetransaction", params, &signedRawTxRes)
			if err != nil {
				return feeReplacementTxArray, txArray, err
			}
			if signedRawTxRes.RPCError != nil {
				b.Logger.Errorf("getportalsignedrawreplacebyfeetransaction: call RPC for ReqTxID %v - with err %v\n", tx.ReqTxID, signedRawTxRes.RPCError.StackTrace)
				continue
			}

			btcTxContent := signedRawTxRes.Result.SignedTx
			btcTxHash := signedRawTxRes.Result.TxID

			txArray[batchID] = &BroadcastTx{
				TxContent:     btcTxContent,
				TxHash:        btcTxHash,
				VSize:         tx.VSize,
				FeePerRequest: tx.FeePerRequest,
				NumOfRequests: tx.NumOfRequests,
				IsBroadcasted: true,
				BlkHeight:     curIncBlkHeight,
			}
		} else {
			txArray[batchID] = &BroadcastTx{
				TxContent:     "",
				TxHash:        "",
				VSize:         tx.VSize,
				FeePerRequest: tx.FeePerRequest,
				NumOfRequests: tx.NumOfRequests,
				IsBroadcasted: false,
				BlkHeight:     curIncBlkHeight,
			}
		}

		delete(feeReplacementTxArray, batchID)
	}

	return feeReplacementTxArray, txArray, nil
}

func (b *BTCBroadcastingManager) submitConfirmedTx(proof string, batchID string) (string, error) {
	metadata := map[string]interface{}{
		"UnshieldProof": proof,
		"PortalTokenID": BTCID,
		"BatchID":       batchID,
	}

	result, err := b.Portal.SubmitConfirmedTx(os.Getenv("INCOGNITO_PRIVATE_KEY"), metadata)
	if err != nil {
		return "", err
	}
	resp, err := b.Client.SubmitRawData(result)
	if err != nil {
		return "", err
	}

	txID, err := transformer.TransformersTxHash(resp)
	if err != nil {
		return "", err
	}
	return txID, nil
}

func (b *BTCBroadcastingManager) getSubmitConfirmedTxStatus(txID string) (int, error) {
	params := []interface{}{
		map[string]string{
			"ReqTxID": txID,
		},
	}

	var confirmedTxStatusRes entities.RequestStatusRes

	var err error
	for idx := 0; idx < NUM_GET_STATUS_TRIES; idx++ {
		time.Sleep(INTERVAL_TRIES)
		err = b.RPCClient.RPCCall("getportalsubmitconfirmedtxstatus", params, &confirmedTxStatusRes)
		if err == nil && confirmedTxStatusRes.RPCError == nil {
			return confirmedTxStatusRes.Result.Status, nil
		}
	}

	if err != nil {
		return 0, err
	} else {
		return 0, fmt.Errorf(confirmedTxStatusRes.RPCError.Message)
	}
}

func (b *BTCBroadcastingManager) requestFeeReplacement(batchID string, newFee uint) (string, error) {
	rpcClient, keyWallet, err := initSetParams(b.RPCClient)
	if err != nil {
		return "", err
	}
	meta, _ := metadata.NewPortalReplacementFeeRequest(PortalReplacementFeeRequestMeta, BTCID, batchID, newFee)
	return sendTx(rpcClient, keyWallet, meta)
}

func (b *BTCBroadcastingManager) getRequestFeeReplacementTxStatus(txID string) (int, error) {
	params := []interface{}{
		map[string]string{
			"ReqTxID": txID,
		},
	}

	var feeReplacementStatusRes entities.RequestStatusRes

	var err error
	for idx := 0; idx < NUM_GET_STATUS_TRIES; idx++ {
		time.Sleep(INTERVAL_TRIES)
		err = b.RPCClient.RPCCall("getportalreplacebyfeestatus", params, &feeReplacementStatusRes)
		if err == nil && feeReplacementStatusRes.RPCError == nil {
			return feeReplacementStatusRes.Result.Status, nil
		}
	}

	if err != nil {
		return 0, err
	} else {
		return 0, fmt.Errorf(feeReplacementStatusRes.RPCError.Message)
	}
}

func joinTxArray(array1 map[string]*BroadcastTx, array2 map[string]*BroadcastTx) map[string]*BroadcastTx {
	joinedArray := map[string]*BroadcastTx{}
	for batchID, value := range array1 {
		joinedArray[batchID] = value
	}
	for batchID, value := range array2 {
		joinedArray[batchID] = value
	}
	return joinedArray
}
