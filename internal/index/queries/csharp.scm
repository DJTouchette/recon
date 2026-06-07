; C# symbols.

(class_declaration name: (identifier) @class) @def
(interface_declaration name: (identifier) @interface) @def
(enum_declaration name: (identifier) @enum) @def
(struct_declaration name: (identifier) @struct) @def
(record_declaration name: (identifier) @class) @def

(method_declaration name: (identifier) @method) @def
(constructor_declaration name: (identifier) @method) @def
(property_declaration name: (identifier) @property) @def
(delegate_declaration name: (identifier) @delegate) @def

(namespace_declaration name: [(identifier) (qualified_name)] @module) @def
