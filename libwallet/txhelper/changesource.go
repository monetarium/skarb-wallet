package txhelper

import (
	"github.com/monetarium/skarb-wallet/libwallet/addresshelper"
	"github.com/monetarium/monetarium-node/dcrutil"
)

const scriptVersion = 0

// TxChangeSource implements Script() and ScriptSize() functions of txauthor.ChangeSource.
type TxChangeSource struct {
	script  []byte
	version uint16
}

func (src *TxChangeSource) Script() ([]byte, uint16, error) {
	return src.script, src.version, nil
}

func (src *TxChangeSource) ScriptSize() int {
	return len(src.script)
}

func MakeTxChangeSource(destAddr string, net dcrutil.AddressParams) (*TxChangeSource, error) {
	pkScript, err := addresshelper.PkScript(destAddr, net)
	if err != nil {
		return nil, err
	}
	return &TxChangeSource{
		script:  pkScript,
		version: scriptVersion,
	}, nil
}
