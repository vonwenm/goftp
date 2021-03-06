// Copyright 2015 Muir Manders.  All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package goftp

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// time.Parse format string for parsing file mtimes.
const timeFormat = "20060102150405"

// Delete delets the file "path".
func (c *Client) Delete(path string) error {
	pconn, err := c.getIdleConn()
	if err != nil {
		return err
	}

	defer c.returnConn(pconn)

	return pconn.sendCommandExpected(replyFileActionOkay, "DELE %s", path)
}

// Rename renames file "from" to "to".
func (c *Client) Rename(from, to string) error {
	pconn, err := c.getIdleConn()
	if err != nil {
		return err
	}

	defer c.returnConn(pconn)

	err = pconn.sendCommandExpected(replyFileActionPending, "RNFR %s", from)
	if err != nil {
		return err
	}

	return pconn.sendCommandExpected(replyFileActionOkay, "RNTO %s", to)
}

// Mkdir creates directory "path". The returned string is how the client
// should refer to the created directory.
func (c *Client) Mkdir(path string) (string, error) {
	pconn, err := c.getIdleConn()
	if err != nil {
		return "", err
	}

	defer c.returnConn(pconn)

	code, msg, err := pconn.sendCommand("MKD %s", path)
	if err != nil {
		return "", err
	}

	if code != replyDirCreated {
		return "", ftpError{code: code, msg: msg}
	}

	dir, err := extractDirName(msg)
	if err != nil {
		return "", err
	}

	return dir, nil
}

// Rmdir removes directory "path".
func (c *Client) Rmdir(path string) error {
	pconn, err := c.getIdleConn()
	if err != nil {
		return err
	}

	defer c.returnConn(pconn)

	return pconn.sendCommandExpected(replyFileActionOkay, "RMD %s", path)
}

// Getwd returns the current working directory.
func (c *Client) Getwd() (string, error) {
	pconn, err := c.getIdleConn()
	if err != nil {
		return "", err
	}

	defer c.returnConn(pconn)

	code, msg, err := pconn.sendCommand("PWD")
	if err != nil {
		return "", err
	}

	if code != replyDirCreated {
		return "", ftpError{code: code, msg: msg}
	}

	dir, err := extractDirName(msg)
	if err != nil {
		return "", err
	}

	return dir, nil
}

// ReadDir fetches the contents of a directory, returning a list of
// os.FileInfo's which are relatively easy to work with programatically. It
// will not return entries corresponding to the current directory or parent
// directories. The os.FileInfo's fields may be incomplete depending on what
// the server supports.
func (c *Client) ReadDir(path string) ([]os.FileInfo, error) {
	entries, err := c.dataStringList("MLSD %s", path)
	if err != nil {
		return nil, err
	}

	var ret []os.FileInfo
	for _, entry := range entries {
		info, err := parseMLST(entry, true)
		if err != nil {
			c.debug("error in ReadDir: %s", err)
			return nil, err
		}

		if info == nil {
			continue
		}

		ret = append(ret, info)
	}

	return ret, nil
}

// Stat fetches details for a particular file. Stat requires the server to
// support the "MLST" feature. The os.FileInfo's fields may be incomplete
// depending on what the server supports.
func (c *Client) Stat(path string) (os.FileInfo, error) {
	lines, err := c.controlStringList("MLST %s", path)
	if err != nil {
		return nil, err
	}

	if len(lines) != 3 {
		return nil, ftpError{err: fmt.Errorf("unexpected MLST response: %v", lines)}
	}

	return parseMLST(strings.TrimLeft(lines[1], " "), false)
}

func extractDirName(msg string) (string, error) {
	openQuote := strings.Index(msg, "\"")
	closeQuote := strings.LastIndex(msg, "\"")
	if openQuote == -1 || len(msg) == openQuote+1 || closeQuote <= openQuote {
		return "", ftpError{
			err: fmt.Errorf("failed parsing directory name: %s", msg),
		}
	}
	return strings.Replace(msg[openQuote+1:closeQuote], `""`, `"`, -1), nil
}

func (c *Client) controlStringList(f string, args ...interface{}) ([]string, error) {
	pconn, err := c.getIdleConn()
	if err != nil {
		return nil, err
	}

	defer c.returnConn(pconn)

	cmd := fmt.Sprintf(f, args...)

	code, msg, err := pconn.sendCommand(cmd)

	if !positiveCompletionReply(code) {
		pconn.debug("unexpected response to %s: %d-%s", cmd, code, msg)
		return nil, ftpError{code: code, msg: msg}
	}

	return strings.Split(msg, "\n"), nil
}

func (c *Client) dataStringList(f string, args ...interface{}) ([]string, error) {
	pconn, err := c.getIdleConn()
	if err != nil {
		return nil, err
	}

	defer c.returnConn(pconn)

	dc, err := pconn.openDataConn()
	if err != nil {
		return nil, err
	}

	// to catch early returns
	defer dc.Close()

	cmd := fmt.Sprintf(f, args...)

	err = pconn.sendCommandExpected(replyGroupPreliminaryReply, cmd)

	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(dc)
	scanner.Split(bufio.ScanLines)

	var res []string
	for scanner.Scan() {
		res = append(res, scanner.Text())
	}

	var dataError error
	if err = scanner.Err(); err != nil {
		pconn.debug("error reading %s data: %s", cmd, err)
		dataError = ftpError{
			err:       fmt.Errorf("error reading %s data: %s", cmd, err),
			temporary: true,
		}
	}

	err = dc.Close()
	if err != nil {
		pconn.debug("error closing data connection: %s", err)
	}

	code, msg, err := pconn.readResponse()
	if err != nil {
		return nil, err
	}

	if !positiveCompletionReply(code) {
		pconn.debug("unexpected result: %d-%s", code, msg)
		return nil, ftpError{code: code, msg: msg}
	}

	if dataError != nil {
		return nil, dataError
	}

	return res, nil
}

type ftpFile struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
	raw   string
}

func (f *ftpFile) Name() string {
	return f.name
}

func (f *ftpFile) Size() int64 {
	return f.size
}

func (f *ftpFile) Mode() os.FileMode {
	return f.mode
}

func (f *ftpFile) ModTime() time.Time {
	return f.mtime
}

func (f *ftpFile) IsDir() bool {
	return f.mode.IsDir()
}

func (f *ftpFile) Sys() interface{} {
	return f.raw
}

// an entry looks something like this:
// type=file;size=12;modify=20150216084148;UNIX.mode=0644;unique=1000004g1187ec7; lorem.txt
func parseMLST(entry string, skipSelfParent bool) (os.FileInfo, error) {
	parseError := ftpError{err: fmt.Errorf(`failed parsing MLST entry: %s`, entry)}
	incompleteError := ftpError{err: fmt.Errorf(`MLST entry incomplete: %s`, entry)}

	parts := strings.Split(entry, "; ")
	if len(parts) != 2 {
		return nil, parseError
	}

	facts := make(map[string]string)
	for _, factPair := range strings.Split(parts[0], ";") {
		factParts := strings.Split(factPair, "=")
		if len(factParts) != 2 {
			return nil, parseError
		}
		facts[strings.ToLower(factParts[0])] = strings.ToLower(factParts[1])
	}

	typ := facts["type"]

	if typ == "" {
		return nil, incompleteError
	}

	if skipSelfParent && (typ == "cdir" || typ == "pdir" || typ == "." || typ == "..") {
		return nil, nil
	}

	var mode os.FileMode
	if facts["unix.mode"] != "" {
		m, err := strconv.ParseInt(facts["unix.mode"], 8, 32)
		if err != nil {
			return nil, parseError
		}
		mode = os.FileMode(m)
	} else if facts["perm"] != "" {
		// see http://tools.ietf.org/html/rfc3659#section-7.5.5
		for _, c := range facts["perm"] {
			switch c {
			case 'a', 'd', 'c', 'f', 'm', 'p', 'w':
				// these suggest you have write permissions
				mode |= 0200
			case 'l':
				// can list dir entries means readable and executable
				mode |= 0500
			case 'r':
				// readable file
				mode |= 0400
			}
		}
	} else {
		// no mode info, just say it's readable to us
		mode = 0400
	}

	if typ == "dir" || typ == "cdir" || typ == "pdir" {
		mode |= os.ModeDir
	}

	var (
		size int64
		err  error
	)

	if facts["size"] != "" {
		size, err = strconv.ParseInt(facts["size"], 10, 64)
	} else if mode.IsDir() && facts["sizd"] != "" {
		size, err = strconv.ParseInt(facts["sizd"], 10, 64)
	} else if facts["type"] == "file" {
		return nil, incompleteError
	}

	if err != nil {
		return nil, parseError
	}

	if facts["modify"] == "" {
		return nil, incompleteError
	}

	mtime, err := time.ParseInLocation(timeFormat, facts["modify"], time.UTC)
	if err != nil {
		return nil, incompleteError
	}

	info := &ftpFile{
		name:  filepath.Base(parts[1]),
		size:  size,
		mtime: mtime,
		raw:   entry,
		mode:  mode,
	}

	return info, nil
}
