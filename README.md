gta: go test auto
================
`gta` builds a list of "dirty" (changed) packages from master, using git.

`gta` works by implementing a various set of interfaces, namely the `Differ` and `Packager` interfaces.

Note: When using this tool, it is common to hit the maximum number of open file descriptors limit set by your OS.
On OSX Yosemite, this is a measily 256, consider raising that maximum to something reasonable with:

```
sudo ulimit -n 4096
```
