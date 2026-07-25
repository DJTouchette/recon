; Shell `source path` / `. path` includes.
;
; The whole argument node is captured, not the string_content inside it:
; tree-sitter splits `"$LIB/util.sh"` into an expansion plus the literal
; "/util.sh", so capturing only the content hands the resolver a path whose
; variable part has silently vanished — and it then resolves the remainder
; against the script's own directory, inventing an edge to the wrong file.
(command
  name: (command_name (word) @_c)
  argument: (_) @path
  (#match? @_c "^(source|\\.)$"))
