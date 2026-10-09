# MessageSchema

```
Message
|------id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY
|------conversation_id BIGINT UNSIGNED NOT NULL
|------role VARCHAR(20) NOT NULL
|------content LONGTEXT NOT NULL
|------created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)

CONSTRAINT fk_messages_conversation
        FOREIGN KEY (conversation_id)
        REFERENCES conversations(id)
        ON DELETE CASCADE
```

