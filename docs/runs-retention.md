# Runs Retention

Run records are retained until an operator prunes them. Use a 90-day default retention window unless a deployment has stricter audit requirements:

```bash
fleet runs prune --older-than 2160h
```

The prune command deletes rows from `runs` whose `started_at` timestamp is older than the requested duration.

Stored LLM transcripts are capped before they are written to `runs.llm_result`. The default cap is 262144 bytes. Set `FLEET_MAX_TRANSCRIPT_BYTES` to a non-negative byte count to tune this per deployment.
