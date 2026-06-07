; Shell `source path` / `. path` includes.
(command
  name: (command_name (word) @_c)
  argument: [(word) @path (string (string_content) @path)]
  (#match? @_c "^(source|\\.)$"))
