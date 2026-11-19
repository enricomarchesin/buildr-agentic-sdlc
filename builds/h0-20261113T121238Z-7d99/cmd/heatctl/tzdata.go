package main

// Embed the time zone database so schedules follow local time even on a
// system without /usr/share/zoneinfo.
import _ "time/tzdata"
