enum State {
 SYSTEM_STATE @description(#"
  The state associated with this system
  - Manages this systems primitives such as Profiles and Catalogs
  - Eventually saved to a database
 "#)

 CLIENT_STATE @description(#"
   Configuration files not owned by mcp gateway
   - Used to manage clients (agents) connection to gateway
 "#)

 RUNTIME_STATE @description(#"
   The ephemeral state of the gateway session:
   - State is lost every time the server is restarted
   - In-memory caching
 "#)

 UNKNOWN_STATE
}

function TypeOfState(content: string) -> State {
  client "CustomSonnet4"
  prompt #"
  Analyze the golang file. Determine the type of
  state it is operating on.
    {{ content }}

    {{ ctx.output_format }}
  "#
}
