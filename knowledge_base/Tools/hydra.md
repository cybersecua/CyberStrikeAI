# hydra

## Overview
- Tool name: `hydra`
- Enabled in config: `true`
- Executable: `hydra`
- Default args: none
- Summary: Password brute force tool supporting multiple protocols and services

## Detailed Description
Hydra is a fast network login cracking tool supporting password brute forcing across multiple protocols and services.

**Key Features:**
- Support for multiple protocols (SSH, FTP, HTTP, SMB, etc.)
- Fast parallel cracking
- Support for username and password wordlists
- Resumable sessions

**Use Cases:**
- Password strength testing
- Penetration testing
- Security assessment
- Weak password detection

## Parameters
### `target`
- Type: `string`
- Required: `true`
- Position: `0`
- Format: `positional`
- Description: Target IP or hostname

### `service`
- Type: `string`
- Required: `true`
- Position: `1`
- Format: `positional`
- Description: Service type (ssh, ftp, http, etc.)

### `username`
- Type: `string`
- Required: `false`
- Flag: `-l`
- Format: `flag`
- Description: Single username

### `username_file`
- Type: `string`
- Required: `false`
- Flag: `-L`
- Format: `flag`
- Description: Username wordlist file

### `password`
- Type: `string`
- Required: `false`
- Flag: `-p`
- Format: `flag`
- Description: Single password

### `password_file`
- Type: `string`
- Required: `false`
- Flag: `-P`
- Format: `flag`
- Description: Password wordlist file

### `additional_args`
- Type: `string`
- Required: `false`
- Format: `positional`
- Description: Additional Hydra parameters

## Invocation Template
```bash
hydra <target> <service> <username> <username_file> <password> <password_file> <additional_args>
```

## Model Usage Guidance
- Prefer the narrowest target/argument set before broad scans.
- For long outputs, store results and summarize key findings.
- Validate parameter formats before execution to reduce command errors.