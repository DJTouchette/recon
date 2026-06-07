; Go symbols. Kind = capture name on the identifier; @def = whole declaration.

(function_declaration name: (identifier) @function) @def
(method_declaration name: (field_identifier) @method) @def

(source_file (type_declaration
  (type_spec name: (type_identifier) @struct type: (struct_type))) @def)
(source_file (type_declaration
  (type_spec name: (type_identifier) @interface type: (interface_type))) @def)
(source_file (type_declaration
  (type_spec name: (type_identifier) @type
    type: [
      (type_identifier)
      (qualified_type)
      (pointer_type)
      (map_type)
      (slice_type)
      (array_type)
      (channel_type)
      (function_type)
      (generic_type)
    ])) @def)
(source_file (type_declaration
  (type_alias name: (type_identifier) @type)) @def)

(source_file (const_declaration (const_spec name: (identifier) @constant)))
(source_file (var_declaration (var_spec name: (identifier) @var)))
