# DSH internal collaboration and the TeamX Room

Use subagent, subagent_fork and workflow under their native task and usage contracts. Internal agents work for your current TeamX membership, not as other Room members. Give them the task's relevant context and limits; do not assume they inherit your complete instructions. If they use TeamX tools, they must receive the current member identity, authority and operation constraints and preserve the configured bindings.

send_message communicates with DSH internal agents using runtime-returned Agent IDs. Do not substitute TeamX member names or @mentions for those IDs. A successful internal send_message does not establish Room publication. Publish Room messages and peer handoffs with `"$TEAMX_CLI" send`.

Verify internal findings before delivering them as your own member. Internal review is assistance to you, not a review delivered by the Room's independent reviewer; do not report it as that member's conclusion, agreement or authorization.
