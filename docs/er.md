
erDiagram
    USERS {
        BIGINT id PK
        VARCHAR username UK
        VARCHAR email UK
        VARCHAR password_hash
        DATETIME created_at
        DATETIME updated_at
    }

    PROJECTS {
        BIGINT id PK
        BIGINT user_id FK
        VARCHAR name
        TEXT description
        VARCHAR status
        DATETIME created_at
        DATETIME updated_at
    }

    CONVERSATIONS {
        BIGINT id PK
        BIGINT user_id FK
        BIGINT project_id FK
        VARCHAR title
        DATETIME created_at
        DATETIME updated_at
    }

    MESSAGES {
        BIGINT id PK
        BIGINT conversation_id FK
        VARCHAR role
        LONGTEXT content
        DATETIME created_at
    }

    TASKS {
        BIGINT id PK
        BIGINT user_id
        BIGINT project_id FK
        BIGINT source_message_id FK
        VARCHAR title
        TEXT description
        VARCHAR status
        INT version
        DATETIME completed_at
        DATETIME created_at
        DATETIME updated_at
    }

    TASK_STATUS_AUDITS {
        BIGINT id PK
        BIGINT task_id FK
        BIGINT actor_user_id FK
        VARCHAR from_status
        VARCHAR to_status
        INT from_version
        INT to_version
        DATETIME created_at
    }

    TASK_EVIDENCE {
        BIGINT id PK
        BIGINT task_id FK
        BIGINT created_by
        VARCHAR type
        TEXT text
        VARCHAR commit_sha
        VARCHAR status
        DATETIME created_at
    }

    TASK_REVIEWS {
        BIGINT id PK
        BIGINT task_id FK
        VARCHAR reviewer_type
        BIGINT reviewer_user_id FK
        VARCHAR decision
        TEXT feedback
        DATETIME created_at
    }

    USERS ||--o{ PROJECTS : owns
    USERS ||--o{ CONVERSATIONS : owns
    PROJECTS ||--o{ CONVERSATIONS : groups
    CONVERSATIONS ||--o{ MESSAGES : contains
    PROJECTS ||--o{ TASKS : contains
    MESSAGES o|--o{ TASKS : originates
    TASKS ||--o{ TASK_STATUS_AUDITS : records
    USERS ||--o{ TASK_STATUS_AUDITS : acts
    TASKS ||--o{ TASK_EVIDENCE : has
    USERS ||--o{ TASK_EVIDENCE : creates
    TASKS ||--o{ TASK_REVIEWS : receives
    USERS o|--o{ TASK_REVIEWS : reviews
