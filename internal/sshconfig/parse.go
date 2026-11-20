package sshconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// keywords is readconf.c's table: every name ssh_config accepts, to the one
// it is stored and printed under. "-" is ignored (Protocol), "-deprecated"
// and "-unsupported" are recognized and dropped.
var keywords = map[string]string{
	"protocol": "-", "cipher": "-deprecated", "fallbacktorsh": "-deprecated", "globalknownhostsfile2": "-deprecated",
	"rhostsauthentication": "-deprecated", "userknownhostsfile2": "-deprecated", "useroaming": "-deprecated",
	"usersh": "-deprecated", "useprivilegedport": "-deprecated",
	"afstokenpassing": "-unsupported", "kerberosauthentication": "-unsupported", "kerberostgtpassing": "-unsupported",
	"rsaauthentication": "-unsupported", "rhostsrsaauthentication": "-unsupported", "compressionlevel": "-unsupported",
	// OpenSSH built without GSSAPI, as the reference one is.
	"gssapiauthentication": "-unsupported", "gssapidelegatecredentials": "-unsupported",
	"pkcs11provider": "pkcs11provider", "smartcarddevice": "pkcs11provider",
	"challengeresponseauthentication": "kbdinteractiveauthentication", "skeyauthentication": "kbdinteractiveauthentication",
	"tisauthentication": "kbdinteractiveauthentication", "dsaauthentication": "pubkeyauthentication",
	"identityfile2": "identityfile", "keepalive": "tcpkeepalive",
	"hostbasedkeytypes": "hostbasedacceptedalgorithms", "pubkeyacceptedkeytypes": "pubkeyacceptedalgorithms",
}

func init() {
	for _, k := range strings.Fields(`forwardagent forwardx11 forwardx11trusted forwardx11timeout
		exitonforwardfailure xauthlocation gatewayports passwordauthentication kbdinteractiveauthentication
		kbdinteractivedevices pubkeyauthentication hostbasedauthentication identityfile identitiesonly
		certificatefile addkeystoagent identityagent hostname hostkeyalias proxycommand port ciphers macs
		remoteforward localforward permitremoteopen user host match tag escapechar globalknownhostsfile
		userknownhostsfile connectionattempts batchmode checkhostip stricthostkeychecking compression
		tcpkeepalive numberofpasswordprompts syslogfacility loglevel logverbose dynamicforward
		preferredauthentications hostkeyalgorithms casignaturealgorithms bindaddress bindinterface
		clearallforwardings enablesshkeysign verifyhostkeydns nohostauthenticationforlocalhost rekeylimit
		connecttimeout addressfamily serveraliveinterval serveralivecountmax sendenv setenv controlpath
		controlmaster controlpersist hashknownhosts include tunnel tunneldevice localcommand
		permitlocalcommand remotecommand visualhostkey kexalgorithms ipqos requesttty sessiontype stdinnull
		forkafterauthentication proxyusefdpass canonicaldomains canonicalizefallbacklocal
		canonicalizehostname canonicalizemaxdots canonicalizepermittedcnames streamlocalbindmask
		streamlocalbindunlink revokedhostkeys fingerprinthash updatehostkeys hostbasedacceptedalgorithms
		pubkeyacceptedalgorithms ignoreunknown proxyjump securitykeyprovider knownhostscommand
		requiredrsasize enableescapecommandline obscurekeystroketiming channeltimeout versionaddendum
		refuseconnection warnweakcrypto`) {
		keywords[k] = k
	}
}

// Known says ssh takes kw (any case) as a keyword it uses: not one it
// ignores, has deprecated or doesn't support.
func Known(kw string) bool {
	v, ok := keywords[strings.ToLower(kw)]
	return ok && !strings.HasPrefix(v, "-")
}

// states is a multistate table: names to values, the first name of a value
// being how it prints.
type states []struct {
	k string
	v int
}

var (
	msFlag     = states{{"true", 1}, {"false", 0}, {"yes", 1}, {"no", 0}}
	msYesNoAsk = append(msFlag[:4:4], states{{"ask", 2}}...)
	msStrict   = states{{"true", 2}, {"false", 0}, {"yes", 2}, {"no", 0}, {"ask", 3}, {"off", 0}, {"accept-new", 1}}
	msConfirm  = append(msYesNoAsk[:5:5], states{{"confirm", 3}}...)
	msFamily   = states{{"inet", afInet}, {"inet6", afInet6}, {"any", afUnspec}}
	msMaster   = states{{"true", 1}, {"yes", 1}, {"false", 0}, {"no", 0}, {"auto", 2}, {"ask", 3}, {"autoask", 4}}
	msTunnel   = states{{"ethernet", 2}, {"point-to-point", 1}, {"true", 1}, {"yes", 1}, {"false", 0}, {"no", 0}}
	msTTY      = states{{"true", 2}, {"yes", 2}, {"false", 1}, {"no", 1}, {"force", 3}, {"auto", 0}}
	msSession  = states{{"none", 0}, {"subsystem", 1}, {"default", 2}}
	msCanon    = states{{"true", 1}, {"false", 0}, {"yes", 1}, {"no", 0}, {"always", 2}}
	msPubkey   = states{{"true", 3}, {"false", 0}, {"yes", 3}, {"no", 0}, {"unbound", 1}, {"host-bound", 2}}
	msCompress = states{{"yes", 2}, {"no", 0}}
	msWeak     = states{{"true", 1}, {"false", 0}, {"yes", 1}, {"no", 0}, {"no-pq-kex", 0}}
)

// multistate is the table each multistate keyword parses with and prints by.
var multistate = map[string]states{
	"pubkeyauthentication": msPubkey, "verifyhostkeydns": msYesNoAsk, "stricthostkeychecking": msStrict,
	"compression": msCompress, "addressfamily": msFamily, "controlmaster": msMaster, "tunnel": msTunnel,
	"requesttty": msTTY, "sessiontype": msSession, "canonicalizehostname": msCanon, "updatehostkeys": msYesNoAsk,
	"warnweakcrypto": msWeak,
}

func (m states) parse(s string) int {
	for _, e := range m {
		if strings.EqualFold(s, e.k) {
			return e.v
		}
	}
	return -1
}

func (m states) name(v int) string {
	for _, e := range m {
		if e.v == v {
			return e.k
		}
	}
	return "UNKNOWN"
}

// Keyword groups by how readconf.c parses them.
var (
	flagKeys = set(`forwardx11 forwardx11trusted gatewayports exitonforwardfailure passwordauthentication
		kbdinteractiveauthentication hostbasedauthentication batchmode checkhostip tcpkeepalive
		nohostauthenticationforlocalhost clearallforwardings enablesshkeysign identitiesonly hashknownhosts
		permitlocalcommand visualhostkey stdinnull forkafterauthentication proxyusefdpass
		canonicalizefallbacklocal streamlocalbindunlink enableescapecommandline`)
	timeKeys   = set(`connecttimeout forwardx11timeout serveraliveinterval`)
	intKeys    = set(`numberofpasswordprompts connectionattempts serveralivecountmax canonicalizemaxdots requiredrsasize`)
	stringKeys = set(`kbdinteractivedevices xauthlocation user hostname tag hostkeyalias preferredauthentications
		bindaddress bindinterface pkcs11provider securitykeyprovider controlpath ignoreunknown`)
	commandKeys = set(`knownhostscommand proxycommand localcommand remotecommand`)
)

func set(s string) map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(s) {
		m[k] = true
	}
	return m
}

var logLevels = states{{"QUIET", 0}, {"FATAL", 1}, {"ERROR", 2}, {"INFO", 3}, {"VERBOSE", 4},
	{"DEBUG", 5}, {"DEBUG1", 5}, {"DEBUG2", 6}, {"DEBUG3", 7}}

var facilities = states{{"DAEMON", 0}, {"USER", 1}, {"AUTH", 2}, {"AUTHPRIV", 3}, {"LOCAL0", 4},
	{"LOCAL1", 5}, {"LOCAL2", 6}, {"LOCAL3", 7}, {"LOCAL4", 8}, {"LOCAL5", 9}, {"LOCAL6", 10}, {"LOCAL7", 11}}

var digests = states{{"MD5", 0}, {"SHA1", 1}, {"SHA256", 2}, {"SHA384", 3}, {"SHA512", 4}}

type idFile struct {
	path string
	user int // readconf.c's userprovided: 2 from a user file, 0 from the system's or defaults
}

type cname struct{ src, dst string }

// options is readconf.c's Options: ints and strs hold what is set (an int
// stored as -1 still counts as unset, as there), keyed by keyword or, for
// the halves of a pair, a name of their own.
type options struct {
	ints map[string]int
	strs map[string]string

	rekeyLimit int64
	identity   []idFile
	certs      []idFile
	sysHosts   []string
	userHosts  []string
	local      []Forward
	remote     []Forward
	remoteOpen []string
	sendEnv    []string
	setEnv     []string
	domains    []string
	cnames     []cname
	revoked    []string
	verbose    []string
	timeouts   []string
	hostArg    string
}

func newOptions() *options {
	return &options{ints: map[string]int{}, strs: map[string]string{}, rekeyLimit: -1}
}

func (o *options) int(k string) int {
	if v, ok := o.ints[k]; ok {
		return v
	}
	return -1
}

func (o *options) setInt(k string, v int, active bool) {
	if active && o.int(k) == -1 {
		o.ints[k] = v
	}
}

func (o *options) has(k string) bool { _, ok := o.strs[k]; return ok }

func (o *options) setStr(k, v string, active bool) {
	if active && !o.has(k) {
		o.strs[k] = v
	}
}

// readconf.c's file flags.
const (
	fCheckPerm  = 1
	fUser       = 2
	fFinal      = 4
	fNeverMatch = 8
)

// fatalError is an error OpenSSH stops at, without reading on.
type fatalError struct{ error }

func fatalf(format string, a ...any) error { return fatalError{fmt.Errorf(format, a...)} }

// parser carries what processing a line needs besides the line itself.
type parser struct {
	r         *resolver
	o         *options
	host      string // what Host matches: the destination, canonicalized in the final pass
	final     bool
	wantFinal *bool // nil in the final pass
}

// args is readconf.c's argv_next/argv_consume over a line's words.
type args []string

func (a *args) next() (string, bool) {
	if len(*a) == 0 {
		return "", false
	}
	s := (*a)[0]
	*a = (*a)[1:]
	return s, true
}

// line is readconf.c's process_config_line_depth for one line of file (or
// a -o option: active nil).
func (p *parser) line(line, file string, num int, active *bool, flags, depth int) error {
	cmdline := active == nil
	if cmdline {
		t := true
		active = &t
	}
	j := len(line)
	for j > 1 && strings.IndexByte(ws+"\f", line[j-1]) >= 0 {
		j--
	}
	line = line[:j]
	kw, str, more, ok := strdelim(line)
	if !ok {
		return nil
	}
	if kw == "" {
		if !more {
			return nil
		}
		if kw, str, more, ok = strdelim(str); !ok {
			return nil
		}
	}
	if kw == "" || kw[0] == '\n' || kw[0] == '#' {
		return nil
	}
	kw = lower(kw)
	str = strings.TrimLeft(str, ws)
	where := fmt.Sprintf("%s line %d", file, num)
	if !more || str == "" {
		return fmt.Errorf("%s: no argument after keyword %q", where, kw)
	}
	op, known := keywords[kw]
	if !known {
		if ig, set := p.o.strs["ignoreunknown"]; !set || matchPatternList(kw, ig, true) != 1 {
			return fmt.Errorf("%s: Bad configuration option: %s", where, kw)
		}
		op = "-"
	}
	av, ok := argvSplit(str)
	if !ok {
		return fmt.Errorf("%s: invalid quotes", where)
	}
	a := args(av)
	if p.r.opt.RelativeToFile && !cmdline {
		anchor(op, file, a)
	}
	if err := p.keyword(op, kw, str, &a, where, active, cmdline, flags, depth); err != nil {
		return err
	}
	if len(a) > 0 {
		return fmt.Errorf("%s: keyword %s extra arguments at end of line", where, kw)
	}
	return nil
}

// relKeys are the path keywords Options.RelativeToFile anchors, and whether
// their values go on through % expansion (pct) and glob.
var relKeys = map[string]struct{ pct, glob bool }{
	"identityfile": {true, false}, "certificatefile": {true, false}, "userknownhostsfile": {true, false},
	"revokedhostkeys": {true, false}, "controlpath": {true, false}, "identityagent": {true, false},
	"forwardagent": {true, false}, "include": {true, true}, "globalknownhostsfile": {false, false},
	"pkcs11provider": {false, false}, "securitykeyprovider": {false, false}, "xauthlocation": {false, false},
}

// anchor puts file's folder before op's relative path words, escaped so the
// expansion and glob they go through leave it as it is.
func anchor(op, file string, a args) {
	k, ok := relKeys[op]
	if !ok {
		return
	}
	dir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return
	}
	if k.pct {
		dir = strings.ReplaceAll(dir, "%", "%%")
	}
	if k.glob && runtime.GOOS != "windows" { // Windows' Glob has no escapes
		for _, c := range `\*?[` {
			dir = strings.ReplaceAll(dir, string(c), `\`+string(c))
		}
	}
	for i, w := range a {
		if relPath(w) {
			a[i] = dir + string(filepath.Separator) + w
		}
	}
}

// relPath is whether w is a relative path, not a word some of relKeys take
// (none, internal, SSH_AUTH_SOCK, ForwardAgent's yes/no).
func relPath(w string) bool {
	switch lower(w) {
	case "", "none", "internal", "ssh_auth_sock", "yes", "no", "true", "false":
		return false
	}
	return !filepath.IsAbs(w) && w[0] != '/' && w[0] != '~' && w[0] != '$' && !strings.HasPrefix(w, "%d")
}

// keyword is the switch of process_config_line_depth: it parses op's
// arguments and, when active, stores them as readconf.c does.
func (p *parser) keyword(op, kw, str string, a *args, where string, active *bool, cmdline bool, flags, depth int) error {
	o, act := p.o, *active
	missing := fmt.Errorf("%s: Missing argument.", where)
	switch {
	case op == "-":
		*a = nil
	case op == "-deprecated":
		p.r.warn(where + ": Deprecated option \"" + kw + "\"")
		*a = nil
	case op == "-unsupported":
		p.r.warn(where + ": Unsupported option \"" + kw + "\"")
		*a = nil
	case timeKeys[op]:
		return p.time(op, a, where, act)
	case flagKeys[op] || multistate[op] != nil:
		m := multistate[op]
		if m == nil {
			m = msFlag
		}
		arg, _ := a.next()
		v := m.parse(arg)
		if arg == "" || v == -1 {
			return fmt.Errorf("%s: unsupported option %q.", where, arg)
		}
		o.setInt(op, v, act)
	case intKeys[op]:
		arg, ok := a.next()
		if !ok || arg == "" {
			return fmt.Errorf("%s: integer value missing.", where)
		}
		v, e := strtonum(arg, 0, math.MaxInt32)
		if e != "" {
			return fmt.Errorf("%s: integer value %s.", where, e)
		}
		o.setInt(op, int(v), act)
	case stringKeys[op]:
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		o.setStr(op, arg, act)
	case commandKeys[op]:
		o.setStr(op, strings.TrimLeft(str, ws+"="), act)
		*a = nil
	default:
		return p.special(op, kw, str, a, where, active, cmdline, flags, depth)
	}
	return nil
}

// time is readconf.c's parse_time: seconds, or none (which leaves it unset).
func (p *parser) time(key string, a *args, where string, act bool) error {
	arg, _ := a.next()
	if arg == "" {
		return fmt.Errorf("%s: missing time value.", where)
	}
	v := -1
	if arg != "none" {
		if v = convtime(arg); v == -1 {
			return fmt.Errorf("%s: invalid time value.", where)
		}
	}
	p.o.setInt(key, v, act)
	return nil
}

// strList parses the list keywords that take "none" alone and keep the
// first line that sets them; a line with no words is fatal when required,
// as readconf.c has it for some.
func strList(a *args, where, kw string, emptyOK, required bool, check func(string) (string, error)) ([]string, error) {
	var out []string
	for {
		arg, ok := a.next()
		if !ok {
			break
		}
		if !emptyOK && arg == "" {
			return nil, fmt.Errorf("%s: keyword %s empty argument", where, kw)
		}
		if strings.EqualFold(arg, "none") && (len(out) > 0 || len(*a) > 0) {
			return nil, fmt.Errorf("%s: keyword %s \"none\" argument must appear alone.", where, kw)
		}
		if check != nil {
			var err error
			if arg, err = check(arg); err != nil {
				return nil, err
			}
		}
		out = append(out, arg)
	}
	if len(out) == 0 && required {
		return nil, fatalf("%s: no %s specified", where, kw)
	}
	return out, nil
}

func (p *parser) special(op, kw, str string, a *args, where string, active *bool, cmdline bool, flags, depth int) error {
	o, act := p.o, *active
	missing := fmt.Errorf("%s: Missing argument.", where)
	switch op {
	case "forwardagent":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		if v := msFlag.parse(arg); v != -1 {
			o.setInt(op, v, act)
			return nil
		}
		o.setInt(op, 1, act)
		return p.agentPath("forwardagentpath", arg, where, act)
	case "identityagent":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		return p.agentPath(op, arg, where, act)
	case "rekeylimit":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		var v int64
		if arg != "default" {
			var ok bool
			if v, ok = scanScaled(arg); !ok {
				return fmt.Errorf("%s: Bad number '%s'", where, arg)
			}
			if v != 0 && v < 16 {
				return fmt.Errorf("%s: RekeyLimit too small", where)
			}
		}
		if act && o.rekeyLimit == -1 {
			o.rekeyLimit = v
		}
		if len(*a) > 0 {
			if (*a)[0] == "none" {
				a.next()
				return nil
			}
			return p.time("rekeyinterval", a, where, act)
		}
	case "identityfile", "certificatefile":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		if !act {
			return nil
		}
		list := &o.identity
		if op == "certificatefile" {
			list = &o.certs
		}
		if len(*list) >= 100 {
			return fmt.Errorf("%s: Too many %s files specified (max 100).", where, strings.TrimSuffix(op, "file"))
		}
		addFile(list, arg, flags&fUser)
	case "globalknownhostsfile", "userknownhostsfile":
		list := &o.sysHosts
		if op == "userknownhostsfile" {
			list = &o.userHosts
		}
		vals, err := strList(a, where, kw, false, false, nil)
		if err != nil {
			return err
		}
		if act && len(*list) == 0 {
			if len(vals) > 32 {
				return fmt.Errorf("%s: too many %s entries.", where, kw)
			}
			*list = vals
		}
	case "port":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		v := a2port(arg)
		if v <= 0 {
			return fmt.Errorf("%s: Bad port '%s'.", where, arg)
		}
		o.setInt(op, v, act)
	case "proxyjump":
		s := strings.TrimLeft(str, ws+"=")
		if !o.parseJump(s, cmdline, act) {
			return fmt.Errorf("%s: Invalid ProxyJump \"%s\"", where, s)
		}
		*a = nil
	case "ciphers", "macs", "kexalgorithms", "hostkeyalgorithms", "casignaturealgorithms",
		"hostbasedacceptedalgorithms", "pubkeyacceptedalgorithms":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		names := strings.TrimLeft(arg[:1], "+^") + arg[1:]
		var valid bool
		switch op {
		case "ciphers":
			valid = namesValid(names, allCiphers)
		case "macs":
			valid = namesValid(names, allMACs)
		case "kexalgorithms":
			valid = namesValid(names, allKex)
		default:
			valid = keyNamesValid(names, op == "casignaturealgorithms")
		}
		if arg[0] != '-' && !valid {
			return fmt.Errorf("%s: Bad %s spec '%s'.", where, kw, arg)
		}
		o.setStr(op, arg, act)
	case "loglevel":
		arg, _ := a.next()
		v := logLevels.parse(arg)
		if v == -1 {
			return fmt.Errorf("%s: unsupported log level '%s'", where, arg)
		}
		o.setInt(op, v, act)
	case "syslogfacility":
		arg, _ := a.next()
		v := facilities.parse(arg)
		if v == -1 {
			return fmt.Errorf("%s: unsupported log facility '%s'", where, arg)
		}
		o.setInt(op, v, true) // readconf.c sets it even in a block that doesn't apply
	case "logverbose":
		vals, err := strList(a, where, kw, false, false, nil)
		if err != nil {
			return err
		}
		if act && len(o.verbose) == 0 && len(vals) > 0 {
			o.verbose = vals[:1] // readconf.c stops appending once the list isn't empty
		}
	case "localforward", "remoteforward", "dynamicforward":
		return p.forward(op, a, where, act)
	case "permitremoteopen":
		var vals []string
		for {
			arg, ok := a.next()
			if !ok {
				break
			}
			if strings.EqualFold(arg, "none") || strings.EqualFold(arg, "any") {
				if len(vals) > 0 || len(*a) > 0 {
					return fmt.Errorf("%s: keyword %s \"%s\" argument must appear alone.", where, kw, arg)
				}
			} else {
				_, port, more, ok := hpdelim(arg)
				if !ok {
					return fatalf("%s: missing host in permitremoteopen", where)
				}
				if !more || port != "*" && a2port(port) <= 0 {
					return fatalf("%s: bad port number in permitremoteopen", where)
				}
			}
			vals = append(vals, arg)
		}
		if len(vals) == 0 {
			return fatalf("%s: missing permitremoteopen specification", where)
		}
		if act && len(o.remoteOpen) == 0 {
			o.remoteOpen = vals
		}
	case "host":
		if cmdline {
			return errors.New("Host directive not supported as a command-line option")
		}
		*active = false
		for {
			arg, ok := a.next()
			if !ok {
				break
			}
			if arg == "" {
				return fmt.Errorf("%s: keyword %s empty argument", where, kw)
			}
			if flags&fNeverMatch != 0 {
				*a = nil
				break
			}
			neg := arg[0] == '!'
			if neg {
				arg = arg[1:]
			}
			if matchPattern(p.host, arg) {
				if neg {
					*active = false
					*a = nil
					break
				}
				*active = true
			}
		}
	case "match":
		if cmdline {
			return errors.New("Match directive not supported as a command-line option")
		}
		v, err := p.match(a, where)
		if err != nil {
			return err
		}
		*active = v && flags&fNeverMatch == 0
	case "escapechar":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		v := 0
		switch {
		case arg == "none":
			v = -2
		case len(arg) == 1:
			v = int(arg[0])
		case len(arg) == 2 && arg[0] == '^' && arg[1] >= 64 && arg[1] < 128:
			v = int(arg[1]) & 31
		default:
			return fmt.Errorf("%s: Bad escape character.", where)
		}
		o.setInt(op, v, act)
	case "sendenv":
		if len(*a) == 0 {
			return fatalf("%s: no %s specified", where, kw)
		}
		for {
			arg, ok := a.next()
			if !ok {
				break
			}
			if arg == "" || strings.Contains(arg, "=") {
				return fmt.Errorf("%s: Invalid environment name.", where)
			}
			if !act {
				continue
			}
			if arg[0] == '-' {
				o.sendEnv = slices.DeleteFunc(o.sendEnv, func(e string) bool { return matchPattern(e, arg[1:]) })
				continue
			}
			o.sendEnv = append(o.sendEnv, arg)
		}
	case "setenv":
		var vals []string
		for {
			arg, ok := a.next()
			if !ok {
				break
			}
			name, _, eq := strings.Cut(arg, "=")
			if !eq {
				return fmt.Errorf("%s: Invalid SetEnv.", where)
			}
			if slices.ContainsFunc(vals, func(v string) bool { return strings.HasPrefix(v, name+"=") }) {
				continue
			}
			vals = append(vals, arg)
		}
		if len(vals) == 0 {
			return fatalf("%s: no %s specified", where, kw)
		}
		if act && len(o.setEnv) == 0 {
			o.setEnv = vals
		}
	case "controlpersist":
		arg, _ := a.next()
		if arg == "" {
			return fmt.Errorf("%s: Missing ControlPersist argument.", where)
		}
		v, t := 0, 0
		switch {
		case arg == "no" || arg == "false":
		case arg == "yes" || arg == "true":
			v = 1
		default:
			if t = convtime(arg); t < 0 {
				return fmt.Errorf("%s: Bad ControlPersist argument.", where)
			}
			v = 1
		}
		if act && o.int(op) == -1 {
			o.ints[op], o.ints["controlpersisttimeout"] = v, t
		}
	case "tunneldevice":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		l, r := a2tun(arg)
		if l == tunErr {
			return fmt.Errorf("%s: Bad tun device.", where)
		}
		if act && o.int("tunlocal") == -1 {
			o.ints["tunlocal"], o.ints["tunremote"] = l, r
		}
	case "include":
		if cmdline {
			return errors.New("Include directive not supported as a command-line option")
		}
		return p.include(a, kw, where, active, flags, depth)
	case "ipqos":
		arg, _ := a.next()
		v := -1
		if arg != "" {
			v = parseIPQoS(arg)
		}
		if v == -1 {
			return fmt.Errorf("%s: Bad IPQoS value: %s", where, arg)
		}
		if v == math.MinInt32 {
			v = math.MaxInt32
		}
		v2 := v
		if arg, ok := a.next(); ok {
			if v2 = parseIPQoS(arg); v2 == -1 {
				return fmt.Errorf("%s: Bad IPQoS value: %s", where, arg)
			}
			if v2 == math.MinInt32 {
				v2 = math.MaxInt32
			}
		}
		if act && o.int(op) == -1 {
			o.ints[op], o.ints["ipqosbulk"] = v, v2
		}
	case "canonicaldomains":
		vals, err := strList(a, where, kw, true, true, func(s string) (string, error) {
			d, bad := validDomain(s, true)
			if bad != "" {
				return "", fmt.Errorf("%s: %s", where, bad)
			}
			return d, nil
		})
		if err != nil {
			return err
		}
		if act && len(o.domains) == 0 {
			o.domains = vals
		}
	case "canonicalizepermittedcnames":
		var vals []cname
		for {
			arg, ok := a.next()
			if !ok {
				break
			}
			switch {
			case strings.EqualFold(arg, "none"):
				if len(vals) > 0 || len(*a) > 0 {
					return fmt.Errorf("%s: keyword %s \"none\" argument must appear alone.", where, kw)
				}
				vals = append(vals, cname{arg, ""})
			case arg == "*":
				vals = append(vals, cname{arg, arg})
			default:
				src, dst, _ := strings.Cut(lower(arg), ":")
				if dst == "" {
					return fmt.Errorf("%s: Invalid permitted CNAME %q", where, lower(arg))
				}
				vals = append(vals, cname{src, dst})
			}
		}
		if len(vals) == 0 {
			return fatalf("%s: no %s specified", where, kw)
		}
		if act && len(o.cnames) == 0 {
			o.cnames = vals
		}
	case "streamlocalbindmask":
		arg, _ := a.next()
		if arg == "" {
			return fmt.Errorf("%s: Missing StreamLocalBindMask argument.", where)
		}
		v, ok := strtolOctal(arg)
		if !ok || v < 0 || v > 0o777 {
			return fmt.Errorf("%s: Bad mask.", where)
		}
		o.ints[op] = v // readconf.c always takes it: the last one wins, applying or not
	case "revokedhostkeys":
		vals, err := strList(a, where, kw, false, true, nil)
		if err != nil {
			return err
		}
		if act && len(o.revoked) == 0 {
			o.revoked = vals
		}
	case "fingerprinthash":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		v := digests.parse(arg)
		if v == -1 {
			return fmt.Errorf("%s: Invalid hash algorithm \"%s\".", where, arg)
		}
		o.setInt(op, v, act)
	case "addkeystoagent":
		arg, _ := a.next()
		arg2, has2 := a.next()
		v, t := -1, 0
		if arg != "" {
			v = msConfirm.parse(arg)
		}
		switch {
		case v == 3 && has2:
			if t = convtime(arg2); t == -1 {
				return fmt.Errorf("%s: invalid time value.", where)
			}
		case v == -1 && !has2:
			if t = convtime(arg); t == -1 {
				return fmt.Errorf("%s: unsupported option", where)
			}
			v = 1
		case v == -1 || has2:
			return fmt.Errorf("%s: unsupported option", where)
		}
		if act && o.int(op) == -1 {
			o.ints[op], o.ints["addkeystoagentlifespan"] = v, t
		}
	case "obscurekeystroketiming":
		v := -1
		for {
			arg, ok := a.next()
			if !ok {
				break
			}
			if v != -1 {
				return fmt.Errorf("%s: invalid arguments", where)
			}
			switch {
			case arg == "yes" || arg == "true":
				v = 20
			case arg == "no" || arg == "false":
				v = 0
			case strings.HasPrefix(arg, "interval:"):
				n, e := strtonum(arg[9:], 0, math.MaxInt32)
				if arg[9:] == "" {
					e = "missing"
				}
				if e != "" {
					return fmt.Errorf("%s: integer value %s.", where, e)
				}
				if n <= 0 || n > 1000 {
					return fmt.Errorf("%s: value out of range.", where)
				}
				v = int(n)
			default:
				return fmt.Errorf("%s: unsupported argument \"%s\"", where, arg)
			}
		}
		if v == -1 {
			return fmt.Errorf("%s: missing argument", where)
		}
		o.setInt(op, v, act)
	case "channeltimeout":
		vals, err := strList(a, where, kw, true, true, func(s string) (string, error) {
			if !strings.EqualFold(s, "none") {
				name, t, eq := strings.Cut(s, "=")
				if !eq || name == "" || convtime(t) < 0 {
					return "", fatalf("%s: invalid channel timeout %s", where, s)
				}
			}
			return s, nil
		})
		if err != nil {
			return err
		}
		if act && len(o.timeouts) == 0 {
			o.timeouts = vals
		}
	case "versionaddendum":
		if strings.Contains(str, "\r") {
			return fatalf("%s: Invalid %s argument", where, kw)
		}
		v := str
		if strings.EqualFold(v, "none") {
			v = ""
		}
		o.setStr(op, v, act)
		*a = nil
	case "refuseconnection":
		arg, _ := a.next()
		if arg == "" {
			return missing
		}
		if act {
			return fatalf("%s: RefuseConnection: %s", where, arg)
		}
	}
	return nil
}

// agentPath is readconf.c's parse_agent_path: ${VAR}s must expand and a
// legacy $VAR must be a name.
func (p *parser) agentPath(key, arg, where string, act bool) error {
	if _, err := expand(arg, nil, true, false); err != nil {
		return fmt.Errorf("%s: Invalid environment expansion %s.", where, arg)
	}
	if strings.HasPrefix(arg, "$") && !strings.HasPrefix(arg, "${") && !validEnvName(arg[1:]) {
		return fmt.Errorf("%s: Invalid environment name %s.", where, arg)
	}
	p.o.setStr(key, arg, act)
	return nil
}

// strtolOctal is strtol(s, &end, 8) where only "no digits at all" fails.
func strtolOctal(s string) (int, bool) {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for n < len(s) && '0' <= s[n] && s[n] <= '7' {
		n++
	}
	if n == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(s[:n], 8, 64)
	if err != nil || v > 0o777 {
		v = 0o1000 // out of range either way
	}
	if neg {
		v = -v
	}
	return int(v), true
}

// addFile is add_identity_file/add_certificate_file: no duplicate from the same kind of file.
func addFile(list *[]idFile, path string, user int) {
	f := idFile{path, user}
	if !slices.Contains(*list, f) {
		*list = append(*list, f)
	}
}

// forward parses LocalForward, RemoteForward and DynamicForward lines.
func (p *parser) forward(op string, a *args, where string, act bool) error {
	arg, _ := a.next()
	if arg == "" {
		return fmt.Errorf("%s: Missing argument.", where)
	}
	remote, dynamic := op == "remoteforward", op == "dynamicforward"
	spec := arg
	if !dynamic {
		arg2, _ := a.next()
		if arg2 == "" {
			if !remote {
				return fmt.Errorf("%s: Missing target argument.", where)
			}
			dynamic = true
		} else {
			spec = arg + ":" + arg2
		}
	}
	if len(spec) > 255 { // readconf.c builds it in a 256-byte buffer
		spec = spec[:255]
	}
	f, ok := parseForward(spec, dynamic, remote)
	if !ok {
		return fmt.Errorf("%s: Bad forwarding specification.", where)
	}
	if act {
		list := &p.o.local
		if remote {
			list = &p.o.remote
		}
		if !slices.Contains(*list, f) {
			*list = append(*list, f)
		}
	}
	return nil
}

// include is readconf.c's Include: each word %-expanded (and ${VAR}), made
// absolute (~/.ssh or the system directory), globbed, and every match read
// at the next depth, in a block that doesn't apply only checked.
func (p *parser) include(a *args, kw, where string, active *bool, flags, depth int) error {
	for {
		arg, ok := a.next()
		if !ok {
			break
		}
		if arg == "" {
			return fmt.Errorf("%s: keyword %s empty argument", where, kw)
		}
		path, err := p.r.expandMatch(arg, p.host, p.final, true)
		if errors.Is(err, errMissingEnv) {
			p.r.warn(fmt.Sprintf("%s: Unable to expand user config file '%s'", where, arg))
			continue
		}
		if err != nil {
			return fatalError{fmt.Errorf("%s: %v", where, err)}
		}
		if strings.HasPrefix(path, "~") && flags&fUser == 0 {
			return fmt.Errorf("%s: bad include path %s.", where, path)
		}
		if !strings.HasPrefix(path, "~") && !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
			if flags&fUser != 0 {
				path = "~/.ssh/" + path
			} else {
				path = p.r.sysDir + "/" + path
			}
		}
		files, err := p.r.glob(path)
		if err != nil {
			return fmt.Errorf("%s: glob failed for %s.", where, path)
		}
		was := *active
		for _, f := range files {
			nf := flags | fCheckPerm
			if !was {
				nf |= fNeverMatch
			}
			// ponytail: a file gone between the glob and the read reads as
			// empty, where ssh counts a bad line; it's a race, not a config.
			if _, err := p.file(f, active, nf, depth+1); err != nil {
				return fmt.Errorf("%s: Include %s: %w", where, f, err)
			}
			*active = was
		}
	}
	return nil
}

// glob is glob(3) with GLOB_TILDE: lexical order, nothing for no match.
func (r *resolver) glob(path string) ([]string, error) {
	if strings.HasPrefix(path, "~") {
		name, rest, _ := strings.Cut(path[1:], "/")
		home := r.home
		if name != "" {
			h, err := lookupUser(name)
			if err != nil {
				return nil, nil
			}
			home = h
		}
		path = strings.TrimSuffix(home, "/") + "/" + rest
	}
	return filepath.Glob(path)
}

// file is readconf.c's read_config_file_depth. opened is false (with err
// the reason, nil when it doesn't exist) when it can't be read.
func (p *parser) file(path string, active *bool, flags, depth int) (opened bool, err error) {
	if depth > 16 {
		return true, fatalf("Too many recursive configuration includes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if fi, serr := os.Stat(path); serr == nil && fi.IsDir() {
			data, err = nil, nil // fopen takes a directory; reading it gives no lines
		} else if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		} else {
			return false, err
		}
	}
	if flags&fCheckPerm != 0 && !permOK(path) {
		return true, fatalf("Bad owner or permissions on %s", path)
	}
	var errs []error
	for i, l := range strings.Split(string(data), "\n") {
		if err := p.line(l, path, i+1, active, flags, depth); err != nil {
			if errors.As(err, new(fatalError)) {
				return true, err
			}
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return true, fatalError{fmt.Errorf("%s: terminating, %d bad configuration options: %w", path, len(errs), errors.Join(errs...))}
	}
	return true, nil
}

// match is readconf.c's match_cfg_line: whether every criterion on the line
// holds, evaluated (and checked) in order.
func (p *parser) match(a *args, where string) (bool, error) {
	r, o := p.r, p.o
	ruser := r.localUser
	if u, ok := o.strs["user"]; ok {
		ruser = u
	}
	host, err := r.matchHost(p.host, p.final)
	if err != nil {
		return false, err
	}
	result, attrs := true, 0
	for {
		attrib, ok := a.next()
		if !ok {
			break
		}
		if strings.HasPrefix(attrib, "#") {
			*a = nil
			break
		}
		oattrib := attrib
		neg := strings.HasPrefix(attrib, "!")
		if neg {
			attrib = attrib[1:]
		}
		if strings.EqualFold(attrib, "all") {
			next, has := a.next()
			if attrs > 1 || has && next != "" && next[0] != '#' {
				return false, fmt.Errorf("%s: '%s' cannot be combined with other Match attributes", where, oattrib)
			}
			if strings.HasPrefix(next, "#") {
				*a = nil
			}
			return result && !neg, nil
		}
		attrs++
		if strings.EqualFold(attrib, "canonical") || strings.EqualFold(attrib, "final") {
			if strings.EqualFold(attrib, "final") && p.wantFinal != nil && !neg {
				*p.wantFinal = true
			}
			if p.final == neg {
				result = false
			}
			continue
		}
		var arg string
		name, val, eq := strings.Cut(attrib, "=")
		switch lower(name) {
		case "host", "originalhost", "user", "localuser", "localnetwork", "version", "tagged", "command", "exec":
		default:
			eq = false
		}
		if eq {
			attrib, arg = name, val
		} else if arg, ok = a.next(); !ok {
			return false, fmt.Errorf("%s: missing argument for Match '%s'", where, oattrib)
		}
		crit := lower(attrib)
		if arg == "" && crit != "tagged" && crit != "command" || strings.HasPrefix(arg, "#") {
			return false, fmt.Errorf("%s: Missing Match criteria for %s", where, attrib)
		}
		var m bool
		switch crit {
		case "host":
			m = matchHostname(host, arg) == 1
		case "originalhost":
			m = matchHostname(o.hostArg, arg) == 1
		case "user":
			m = matchPatternList(ruser, arg, false) == 1
		case "localuser":
			m = matchPatternList(r.localUser, arg, false) == 1
		case "localnetwork":
			if matchCIDRList(nil, arg) == -1 {
				return false, fmt.Errorf("%s: Bad Match condition: localnetwork %s", where, arg)
			}
			for _, n := range r.opt.Networks() {
				if ip, ok := netipFrom(n.IP); ok && matchCIDRList(&ip, arg) == 1 {
					m = true
					break
				}
			}
		case "version":
			m = matchPatternList(release, arg, false) == 1
		case "tagged":
			tag := o.strs["tag"]
			m = tag == "" && arg == "" || tag != "" && matchPatternList(tag, arg, false) == 1
		case "command":
			m = arg == "" // ponytail: no remote command at resolve time; add Options.Command when the client runs one
		case "sessiontype":
			t := "shell"
			switch o.int("sessiontype") {
			case 1:
				t = "subsystem"
			case 0:
				t = "none"
			}
			m = matchPatternList(t, arg, false) == 1
		case "exec":
			cmd, err := r.expandMatch(arg, p.host, p.final, false)
			if err != nil {
				return false, fatalf("%s: failed to expand match exec '%s'", where, arg)
			}
			if !result {
				continue
			}
			if m, err = r.opt.Exec(cmd); err != nil {
				return false, fatalf("%s: match exec '%s' error: %v", where, cmd, err)
			}
		default:
			return false, fmt.Errorf("%s: Unsupported Match attribute %s", where, attrib)
		}
		if m == neg {
			result = false
		}
	}
	if attrs == 0 {
		return false, fmt.Errorf("%s: One or more attributes required for Match", where)
	}
	return result, nil
}
