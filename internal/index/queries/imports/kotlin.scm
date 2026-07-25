; Kotlin import declarations. @path captures the dotted qualified identifier
; (com.example.Repository).
;
; The Kotlin grammar does not give the trailing ".*" of an on-demand import a
; node of its own, so @stmt carries the whole statement text and the resolver
; detects the wildcard from it.
(import (qualified_identifier) @path) @stmt
