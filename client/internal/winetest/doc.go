// Package winetest holds Windows stand-ins used by scripts/wine-test.sh to
// test nulldrive:// link handling and the installer under Wine:
//
//	runningcopy  creates the single-instance mutex and message-only window a
//	             running Null Drive has, and prints what a second launch sends
//	recorder     a fake NullDrive.exe that records its command line
//	shellexec    opens a URL with ShellExecute, as a browser does
//
// They are tools, not part of the app.
package winetest
