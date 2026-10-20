# Architecture

[Docs index](README.md) · [Canvas overview](../README.md)

Canvas owns document review; Sessions owns durable execution. The current local
path and the hosted direction below are different assemblies, not two supported
deployment options.

## Today: local engine with embedded Sessions

```mermaid
flowchart TB
    P["Copilot app panel"] --> B["Local HTTP adapter<br/>transport capability, not human identity"]
    W["Browser fallback"] --> E
    B -->|"loopback HTTP"| E
    D["canvas.yaml, schemas, seeds<br/>digest-pinned suite layouts"] --> E
    subgraph Local["One local process; one trusted operator"]
        E["Canvas engine<br/>proposals, review and action requests"]
        G[("SharedSpace<br/>Git documents + review ledger")]
        S["Embedded Sessions host<br/>journal, action approval and replay"]
        H["Local bundle harness<br/>suite instructions and document output"]
        J[("Sessions journal<br/>SQLite")]
        E <--> G
        E -->|"runs, decisions, explicit Continue"| S
        S <--> H
        S <--> J
        S --> T["Host-owned model provider<br/>deterministic OR model-backed"]
        S --> X["Separate approval-gated<br/>local action simulation"]
    end
    T -->|"model-backed mode only"| M["Configured local / hosted<br/>model endpoint"]
```

**Legend:** solid boxes and arrows describe implemented components and paths,
not live-model or deployment qualification. The Copilot demo uses the deterministic
provider; model-backed startup uses the same engine with a configured provider.
The panel and browser do not call Sessions or the model directly.
See the [local review guide](copilot-review.md) and
[model-backed setup](local-models.md) for their separate commands and state roots.

## Planned: separate hosted engine

This is the intended hosted assembly, **not an install guide**. Dashed boxes and
arrows mean planned assembly, packaging or integration, even where the underlying
engine or Sessions component already exists.

```mermaid
flowchart TB
    P["Copilot panel + thin local adapter"] -.->|"planned: TLS API + browser sign-in"| E
    I["OIDC identity provider"] -.->|"planned: human access tokens"| E
    C["Canvas OCI package<br/>definition + pinned AgentSuite dependencies"] -.->|"planned: activation"| E
    subgraph AKS["Planned AKS assembly in aks-durable-agents"]
        E["Canvas engine Deployment<br/>one instance, one writer"]
        G[("Private engine PVC<br/>Git, reviews, ledgers, retained definitions")]
        S["Private Sessions host<br/>journal, approvals and replay"]
        H["Generic document-producer harness<br/>one pinned suite member per run"]
        J[("Host state<br/>journal, model ledger and receipts")]
        E -.-> G
        E -.->|"planned: runs + forwarded human tokens"| S
        S -.->|"planned: turns and model/tool requests"| H
        S -.-> J
    end
    S -.->|"planned: host-owned access"| M["Model + tool executors"]
    G -.-> R["Planned coordinated recovery set<br/>fenced engine + host / harness state"]
    J -.-> R
    classDef planned stroke-dasharray: 5 5
    class P,I,C,E,G,S,H,J,M,R planned
```

## Main boundaries

- **Documents:** SharedSpace owns accepted Git history, proposals and a separate review ledger; acceptance uses stale-base checks, not live co-editing.
- **Execution:** Sessions owns the journal and recorded replay; document history does not replace execution history.
- **Approvals:** accepting documents, approving an action and explicitly continuing are separate operations; approval alone does not execute a tool.
- **Human identity:** demo personas and Copilot sign-in are not authority; the opt-in [TLS/token-source path](authenticated-sessions.md) is not per-browser shared-user sign-in.
- **Hosted identity (planned):** Canvas obtains and forwards human tokens; Sessions verifies them directly where the provider issues Sessions-audience tokens, with operator-owned role policy.
- **Agents and models:** definitions pin suite members and document scopes; the current provider is host-owned, while sandboxed generic execution and host-mediated model/tool access are hosted integration work.
- **Recovery (planned):** keep one engine writer; fence and back up its whole private state together with Sessions journals, ledgers and receipts, then reconcile before enabling writes.

The current generic command rejects a remote Sessions address. A hosted engine,
Canvas OCI activation, browser sign-in, coordinated AKS restore and multi-reviewer
authorization remain work to do; see the [roadmap](../ROADMAP.md).
Git history is not a tamper-proof audit log, and a PVC alone is not writer fencing.
