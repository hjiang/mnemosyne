## Introduction

This is a self-hosted web application that pulls emails from IMAP servers and make them searchable.

## Functionality

- The user has to log in to use the application. Multiple users are
  supported. Each user cannot access other users' data.
- The user can configure multiple IMAP accounts to backup.
- For each IMAP account, the user can select any number of folders to backup.
- For each folder, the user can define a backup policy, such as whether
  backed-up mails are left on the server, how many to leave on the server, or
  mails before how long ago to leave on the server.
- The backup job runs once a day by default and can be manually triggered.
- Emails that are already backed up are not downloaded repeated across multiple
  runs.
- Emails that exist in multiple folders are not downloaded multiple times.
- Attachments are also downloaded and backed up.
- Support Gmail style search ("from:example.com", etc.) as well as full-text
  search.
- Full-text search searches both the emails and the attachments.
- Emails can be batch exported as mbox, Maildir, or to an IMAP folder.

## UI

- Use a minimalist and elegant design.

## Deployment

- The application should be lightweight and easy to deploy to a Linux server.
