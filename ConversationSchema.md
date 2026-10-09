# ConversationSchema

```
Conversation
|------id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY
|------user_id BIGINT UNSIGNED NOT NULL
|------title VARCHAR(255) NOT NULL
|------created_at DATETIME(3) NOT NULL
|------updated_at DATETIME(3) NOT NULL

INDEX idx_conversations_user_id (user_id),

    CONSTRAINT fk_conversations_user
        FOREIGN KEY (user_id)
        REFERENCES users(id)
        ON DELETE CASCADE
```

