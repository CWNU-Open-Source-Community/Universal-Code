use std::{
    collections::{BTreeSet, HashSet},
    io::{self, Read, Write},
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

pub const REQUEST_PROTOCOL_VERSION: &str = "analyzer_protocol.v1";
pub const RESULT_PROTOCOL_VERSION: &str = "analyzer_result.v1";
pub const ERROR_PROTOCOL_VERSION: &str = "analyzer_error.v1";
pub const FIXTURE_ANALYZER_NAME: &str = "fixture.digest.v1";
pub const ARCHIVE_INVENTORY_PROTOCOL_VERSION: &str = "archive.inventory.v1";
pub const ARCHIVE_ANALYZER_NAME: &str = "archive.zip.inventory.v1";

pub const MAX_REQUEST_ENVELOPE_BYTES: usize = 96 * 1024;
pub const MAX_DECODED_INPUT_BYTES: usize = 64 * 1024;
pub const MIN_RESULT_ENVELOPE_BYTES: usize = 512;
pub const MAX_RESULT_ENVELOPE_BYTES: usize = 16 * 1024;
pub const MIN_TIMEOUT_MILLISECONDS: i64 = 100;
pub const MAX_TIMEOUT_MILLISECONDS: i64 = 30_000;
pub const MAX_REQUEST_ID_BYTES: usize = 128;
pub const MAX_MEDIA_TYPE_BYTES: usize = 128;

pub const MAX_ARCHIVE_ENTRIES: usize = 32;
pub const MAX_ARCHIVE_ENTRY_NAME_BYTES: usize = 128;
pub const MAX_ARCHIVE_TOTAL_NAME_BYTES: usize = 2 * 1024;
pub const MAX_ARCHIVE_DECLARED_ENTRY_BYTES: u64 = 8 * 1024 * 1024;
pub const MAX_ARCHIVE_DECLARED_TOTAL_BYTES: u64 = 32 * 1024 * 1024;
pub const MAX_ARCHIVE_COMPRESSION_RATIO_MILLI: u64 = 100_000;
pub const MAX_ARCHIVE_REPORTED_RATIO_MILLI: u64 = 1_000_000_000;

pub const EXIT_SUCCESS: u8 = 0;
pub const EXIT_REJECTED: u8 = 2;
pub const EXIT_INTERNAL: u8 = 3;

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorCode {
    MalformedEnvelope,
    RequestTooLarge,
    UnsupportedProtocol,
    InvalidRequest,
    CapabilityDenied,
    UnsupportedAnalyzer,
    InputLimitExceeded,
    InvalidContent,
    OutputLimitExceeded,
    ResultTooLarge,
    DeadlineExceeded,
    ProcessFailed,
    InvalidResult,
    InternalError,
}

pub const ALL_ERROR_CODES: [ErrorCode; 14] = [
    ErrorCode::MalformedEnvelope,
    ErrorCode::RequestTooLarge,
    ErrorCode::UnsupportedProtocol,
    ErrorCode::InvalidRequest,
    ErrorCode::CapabilityDenied,
    ErrorCode::UnsupportedAnalyzer,
    ErrorCode::InputLimitExceeded,
    ErrorCode::InvalidContent,
    ErrorCode::OutputLimitExceeded,
    ErrorCode::ResultTooLarge,
    ErrorCode::DeadlineExceeded,
    ErrorCode::ProcessFailed,
    ErrorCode::InvalidResult,
    ErrorCode::InternalError,
];

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Capabilities {
    pub filesystem: bool,
    pub network: bool,
    pub subprocess: bool,
    pub environment: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Limits {
    pub max_input_bytes: i64,
    pub max_output_bytes: i64,
    pub timeout_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Input {
    pub media_type: String,
    pub content_base64: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub protocol_version: String,
    pub request_id: String,
    pub analyzer: String,
    pub input: Input,
    pub limits: Limits,
    pub capabilities: Capabilities,
    pub metadata_only: bool,
}

#[derive(Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Summary {
    pub media_type: String,
    pub input_bytes: usize,
    pub sha256: String,
    pub utf8: bool,
    pub line_count: usize,
}

#[derive(Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ResultEnvelope {
    pub protocol_version: String,
    pub request_id: String,
    pub analyzer: String,
    pub status: String,
    pub summary: Summary,
    pub metadata_only: bool,
    pub capabilities_used: Capabilities,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ArchiveRiskCode {
    AbsolutePath,
    BackslashSeparator,
    CompressionRatio,
    DeclaredEntrySize,
    DeclaredTotalSize,
    DirectoryHasData,
    DuplicateName,
    ParentTraversal,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ArchiveInventoryLimits {
    pub max_entries: usize,
    pub max_entry_name_bytes: usize,
    pub max_total_name_bytes: usize,
    pub max_declared_entry_bytes: u64,
    pub max_declared_total_bytes: u64,
    pub max_compression_ratio_milli: u64,
    pub max_reported_ratio_milli: u64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ArchiveEntry {
    pub index: usize,
    pub name: String,
    pub kind: String,
    pub compressed_bytes: u64,
    pub uncompressed_bytes: u64,
    pub compression_ratio_milli: u64,
    pub declared_crc32: String,
    pub risk_codes: Vec<ArchiveRiskCode>,
}

#[derive(Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ArchiveInventory {
    pub protocol_version: String,
    pub request_id: String,
    pub analyzer: String,
    pub status: String,
    pub format: String,
    pub entry_count: usize,
    pub total_compressed_bytes: u64,
    pub total_uncompressed_bytes: u64,
    pub limits: ArchiveInventoryLimits,
    pub entries: Vec<ArchiveEntry>,
    pub risk_entry_count: usize,
    pub risk_codes: Vec<ArchiveRiskCode>,
    pub metadata_only: bool,
    pub central_directory_only: bool,
    pub entry_contents_read: bool,
    pub extraction_performed: bool,
    pub capabilities_used: Capabilities,
}

#[derive(Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ErrorEnvelope {
    pub protocol_version: String,
    pub request_id: String,
    pub code: ErrorCode,
    pub retryable: bool,
    pub message: String,
    pub metadata_only: bool,
}

#[derive(Debug, Eq, PartialEq)]
pub struct Evaluation {
    pub stdout: Vec<u8>,
    pub exit_code: u8,
}

pub fn evaluate(raw: &[u8]) -> Evaluation {
    if raw.len() > MAX_REQUEST_ENVELOPE_BYTES {
        return rejection("", ErrorCode::RequestTooLarge);
    }
    if raw.is_empty() {
        return rejection("", ErrorCode::MalformedEnvelope);
    }
    let wire: RequestWire = match serde_json::from_slice(raw) {
        Ok(value) => value,
        Err(_) => return rejection("", ErrorCode::MalformedEnvelope),
    };
    let request = match wire.into_request() {
        Ok(value) => value,
        Err(code) => return rejection("", code),
    };
    if let Some(code) = validate_request(&request) {
        return rejection(safe_request_id(&request.request_id), code);
    }
    let content = match decode_content(&request) {
        Ok(value) => value,
        Err(code) => return rejection(&request.request_id, code),
    };
    if request.analyzer == ARCHIVE_ANALYZER_NAME {
        return evaluate_archive(request, &content);
    }
    evaluate_fixture(request, &content)
}

fn evaluate_fixture(request: Request, content: &[u8]) -> Evaluation {
    let text = std::str::from_utf8(content).is_ok();
    let result = ResultEnvelope {
        protocol_version: RESULT_PROTOCOL_VERSION.to_owned(),
        request_id: request.request_id.clone(),
        analyzer: request.analyzer,
        status: "succeeded".to_owned(),
        summary: Summary {
            media_type: request.input.media_type,
            input_bytes: content.len(),
            sha256: Sha256::digest(content)
                .iter()
                .map(|b| format!("{:02x}", b))
                .collect::<String>(),
            utf8: text,
            line_count: logical_line_count(content, text),
        },
        metadata_only: true,
        capabilities_used: disabled_capabilities(),
    };
    let encoded = match serde_json::to_vec(&result) {
        Ok(value) => value,
        Err(_) => return rejection(&request.request_id, ErrorCode::InternalError),
    };
    if encoded.len() > request.limits.max_output_bytes as usize
        || encoded.len() > MAX_RESULT_ENVELOPE_BYTES
    {
        return rejection(&request.request_id, ErrorCode::OutputLimitExceeded);
    }
    Evaluation {
        stdout: encoded,
        exit_code: EXIT_SUCCESS,
    }
}

fn evaluate_archive(request: Request, content: &[u8]) -> Evaluation {
    let inventory = match inventory_zip(&request.request_id, content) {
        Ok(value) => value,
        Err(code) => return rejection(&request.request_id, code),
    };
    let encoded = match serde_json::to_vec(&inventory) {
        Ok(value) => value,
        Err(_) => return rejection(&request.request_id, ErrorCode::InternalError),
    };
    if encoded.len() > request.limits.max_output_bytes as usize
        || encoded.len() > MAX_RESULT_ENVELOPE_BYTES
    {
        return rejection(&request.request_id, ErrorCode::OutputLimitExceeded);
    }
    Evaluation {
        stdout: encoded,
        exit_code: EXIT_SUCCESS,
    }
}

fn inventory_zip(request_id: &str, content: &[u8]) -> Result<ArchiveInventory, ErrorCode> {
    let archive = rawzip::ZipArchive::from_slice(content).map_err(|_| ErrorCode::InvalidContent)?;
    if archive.entries_hint() > MAX_ARCHIVE_ENTRIES as u64 {
        return Err(ErrorCode::InputLimitExceeded);
    }
    let mut entries = Vec::with_capacity(archive.entries_hint() as usize);
    for (index, record) in archive.entries().enumerate() {
        if index >= MAX_ARCHIVE_ENTRIES {
            return Err(ErrorCode::InputLimitExceeded);
        }
        let record = record.map_err(|_| ErrorCode::InvalidContent)?;
        let name = std::str::from_utf8(record.file_path().as_bytes())
            .map_err(|_| ErrorCode::InvalidContent)?
            .to_owned();
        entries.push(ArchiveEntry {
            index,
            kind: archive_entry_kind(&name).to_owned(),
            name,
            compressed_bytes: record.compressed_size_hint(),
            uncompressed_bytes: record.uncompressed_size_hint(),
            compression_ratio_milli: archive_ratio_milli(
                record.uncompressed_size_hint(),
                record.compressed_size_hint(),
            ),
            declared_crc32: format!("{:08x}", record.crc32()),
            risk_codes: Vec::new(),
        });
    }
    if entries.len() as u64 != archive.entries_hint() {
        return Err(ErrorCode::InvalidContent);
    }
    build_archive_inventory(request_id, entries)
}

fn build_archive_inventory(
    request_id: &str,
    mut entries: Vec<ArchiveEntry>,
) -> Result<ArchiveInventory, ErrorCode> {
    if entries.len() > MAX_ARCHIVE_ENTRIES {
        return Err(ErrorCode::InputLimitExceeded);
    }
    let mut seen_names = HashSet::with_capacity(entries.len());
    let mut aggregate_risks = BTreeSet::new();
    let mut total_names = 0usize;
    let mut total_compressed = 0u64;
    let mut total_uncompressed = 0u64;
    let mut risk_entry_count = 0usize;
    for (index, entry) in entries.iter_mut().enumerate() {
        if entry.index != index {
            return Err(ErrorCode::InputLimitExceeded);
        }
        if !valid_archive_entry_name(&entry.name) {
            return Err(ErrorCode::InvalidContent);
        }
        if entry.name.len() > MAX_ARCHIVE_ENTRY_NAME_BYTES {
            return Err(ErrorCode::InputLimitExceeded);
        }
        total_names = total_names.saturating_add(entry.name.len());
        if total_names > MAX_ARCHIVE_TOTAL_NAME_BYTES {
            return Err(ErrorCode::InputLimitExceeded);
        }
        entry.kind = archive_entry_kind(&entry.name).to_owned();
        entry.compression_ratio_milli =
            archive_ratio_milli(entry.uncompressed_bytes, entry.compressed_bytes);
        if !valid_crc32(&entry.declared_crc32) {
            return Err(ErrorCode::InvalidContent);
        }
        entry.risk_codes = archive_entry_risks(entry, &seen_names);
        if !entry.risk_codes.is_empty() {
            risk_entry_count += 1;
        }
        aggregate_risks.extend(entry.risk_codes.iter().copied());
        seen_names.insert(entry.name.clone());
        total_compressed = total_compressed.saturating_add(entry.compressed_bytes);
        total_uncompressed = total_uncompressed.saturating_add(entry.uncompressed_bytes);
    }
    if total_uncompressed > MAX_ARCHIVE_DECLARED_TOTAL_BYTES {
        aggregate_risks.insert(ArchiveRiskCode::DeclaredTotalSize);
    }
    Ok(ArchiveInventory {
        protocol_version: ARCHIVE_INVENTORY_PROTOCOL_VERSION.to_owned(),
        request_id: request_id.to_owned(),
        analyzer: ARCHIVE_ANALYZER_NAME.to_owned(),
        status: "succeeded".to_owned(),
        format: "zip".to_owned(),
        entry_count: entries.len(),
        total_compressed_bytes: total_compressed,
        total_uncompressed_bytes: total_uncompressed,
        limits: archive_inventory_limits(),
        entries,
        risk_entry_count,
        risk_codes: aggregate_risks.into_iter().collect(),
        metadata_only: true,
        central_directory_only: true,
        entry_contents_read: false,
        extraction_performed: false,
        capabilities_used: disabled_capabilities(),
    })
}

fn archive_inventory_limits() -> ArchiveInventoryLimits {
    ArchiveInventoryLimits {
        max_entries: MAX_ARCHIVE_ENTRIES,
        max_entry_name_bytes: MAX_ARCHIVE_ENTRY_NAME_BYTES,
        max_total_name_bytes: MAX_ARCHIVE_TOTAL_NAME_BYTES,
        max_declared_entry_bytes: MAX_ARCHIVE_DECLARED_ENTRY_BYTES,
        max_declared_total_bytes: MAX_ARCHIVE_DECLARED_TOTAL_BYTES,
        max_compression_ratio_milli: MAX_ARCHIVE_COMPRESSION_RATIO_MILLI,
        max_reported_ratio_milli: MAX_ARCHIVE_REPORTED_RATIO_MILLI,
    }
}

fn archive_entry_risks(entry: &ArchiveEntry, seen_names: &HashSet<String>) -> Vec<ArchiveRiskCode> {
    let mut risks = BTreeSet::new();
    if archive_absolute_path(&entry.name) {
        risks.insert(ArchiveRiskCode::AbsolutePath);
    }
    if entry.name.contains('\\') {
        risks.insert(ArchiveRiskCode::BackslashSeparator);
    }
    if archive_parent_traversal(&entry.name) {
        risks.insert(ArchiveRiskCode::ParentTraversal);
    }
    if seen_names.contains(&entry.name) {
        risks.insert(ArchiveRiskCode::DuplicateName);
    }
    if entry.uncompressed_bytes > MAX_ARCHIVE_DECLARED_ENTRY_BYTES {
        risks.insert(ArchiveRiskCode::DeclaredEntrySize);
    }
    if entry.compression_ratio_milli > MAX_ARCHIVE_COMPRESSION_RATIO_MILLI {
        risks.insert(ArchiveRiskCode::CompressionRatio);
    }
    if entry.kind == "directory" && (entry.compressed_bytes != 0 || entry.uncompressed_bytes != 0) {
        risks.insert(ArchiveRiskCode::DirectoryHasData);
    }
    risks.into_iter().collect()
}

fn archive_entry_kind(name: &str) -> &'static str {
    if name.ends_with('/') || name.ends_with('\\') {
        "directory"
    } else {
        "file"
    }
}

fn valid_archive_entry_name(name: &str) -> bool {
    !name.is_empty() && name.bytes().all(|byte| byte >= 0x20 && byte != 0x7f)
}

fn archive_absolute_path(name: &str) -> bool {
    let bytes = name.as_bytes();
    name.starts_with('/')
        || name.starts_with('\\')
        || (bytes.len() >= 2 && bytes[0].is_ascii_alphabetic() && bytes[1] == b':')
}

fn archive_parent_traversal(name: &str) -> bool {
    name.split(['/', '\\']).any(|part| part == "..")
}

fn archive_ratio_milli(uncompressed: u64, compressed: u64) -> u64 {
    if uncompressed == 0 {
        return 0;
    }
    if compressed == 0 {
        return MAX_ARCHIVE_REPORTED_RATIO_MILLI;
    }
    let scaled = (u128::from(uncompressed) * 1000) / u128::from(compressed);
    scaled.min(u128::from(MAX_ARCHIVE_REPORTED_RATIO_MILLI)) as u64
}

fn valid_crc32(value: &str) -> bool {
    value.len() == 8
        && value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

pub fn run_io<R: Read, W: Write>(reader: R, mut writer: W) -> io::Result<u8> {
    let mut raw = Vec::with_capacity(4096);
    reader
        .take((MAX_REQUEST_ENVELOPE_BYTES + 1) as u64)
        .read_to_end(&mut raw)?;
    let evaluation = evaluate(&raw);
    writer.write_all(&evaluation.stdout)?;
    writer.flush()?;
    Ok(evaluation.exit_code)
}

pub fn internal_error() -> Vec<u8> {
    encode_error("", ErrorCode::InternalError)
}

fn validate_request(request: &Request) -> Option<ErrorCode> {
    if request.protocol_version != REQUEST_PROTOCOL_VERSION {
        return Some(ErrorCode::UnsupportedProtocol);
    }
    if !valid_request_id(&request.request_id) {
        return Some(ErrorCode::InvalidRequest);
    }
    if request.analyzer != FIXTURE_ANALYZER_NAME && request.analyzer != ARCHIVE_ANALYZER_NAME {
        return Some(ErrorCode::UnsupportedAnalyzer);
    }
    if capabilities_enabled(&request.capabilities) {
        return Some(ErrorCode::CapabilityDenied);
    }
    if !request.metadata_only
        || request.limits.max_input_bytes < 1
        || request.limits.max_input_bytes > MAX_DECODED_INPUT_BYTES as i64
        || request.limits.max_output_bytes < MIN_RESULT_ENVELOPE_BYTES as i64
        || request.limits.max_output_bytes > MAX_RESULT_ENVELOPE_BYTES as i64
        || request.limits.timeout_ms < MIN_TIMEOUT_MILLISECONDS
        || request.limits.timeout_ms > MAX_TIMEOUT_MILLISECONDS
        || !valid_media_type(&request.input.media_type)
        || (request.analyzer == ARCHIVE_ANALYZER_NAME
            && request.input.media_type != "application/zip")
    {
        return Some(ErrorCode::InvalidRequest);
    }
    decode_content(request).err()
}

fn decode_content(request: &Request) -> Result<Vec<u8>, ErrorCode> {
    let maximum_encoded = MAX_DECODED_INPUT_BYTES.div_ceil(3) * 4;
    if request.input.content_base64.len() > maximum_encoded {
        return Err(ErrorCode::InputLimitExceeded);
    }
    let content = STANDARD
        .decode(&request.input.content_base64)
        .map_err(|_| ErrorCode::InvalidContent)?;
    if STANDARD.encode(&content) != request.input.content_base64 {
        return Err(ErrorCode::InvalidContent);
    }
    if content.len() > request.limits.max_input_bytes as usize
        || content.len() > MAX_DECODED_INPUT_BYTES
    {
        return Err(ErrorCode::InputLimitExceeded);
    }
    Ok(content)
}

fn rejection(request_id: &str, code: ErrorCode) -> Evaluation {
    Evaluation {
        stdout: encode_error(request_id, code),
        exit_code: EXIT_REJECTED,
    }
}

fn encode_error(request_id: &str, code: ErrorCode) -> Vec<u8> {
    serde_json::to_vec(&ErrorEnvelope {
        protocol_version: ERROR_PROTOCOL_VERSION.to_owned(),
        request_id: request_id.to_owned(),
        code,
        retryable: retryable(code),
        message: message_for(code).to_owned(),
        metadata_only: true,
    })
    .expect("fixed analyzer error envelope must serialize")
}

fn logical_line_count(content: &[u8], text: bool) -> usize {
    if !text || content.is_empty() {
        return 0;
    }
    let mut count = content.iter().filter(|byte| **byte == b'\n').count();
    if content.last() != Some(&b'\n') {
        count += 1;
    }
    count
}

fn disabled_capabilities() -> Capabilities {
    Capabilities {
        filesystem: false,
        network: false,
        subprocess: false,
        environment: false,
    }
}

fn capabilities_enabled(value: &Capabilities) -> bool {
    value.filesystem || value.network || value.subprocess || value.environment
}

fn valid_request_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= MAX_REQUEST_ID_BYTES
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b':' | b'-'))
}

fn safe_request_id(value: &str) -> &str {
    if valid_request_id(value) { value } else { "" }
}

fn valid_media_type(value: &str) -> bool {
    if value.len() < 3 || value.len() > MAX_MEDIA_TYPE_BYTES || value.matches('/').count() != 1 {
        return false;
    }
    value.split('/').all(|part| {
        !part.is_empty()
            && part.bytes().all(|byte| {
                byte.is_ascii_lowercase()
                    || byte.is_ascii_digit()
                    || matches!(
                        byte,
                        b'!' | b'#' | b'$' | b'&' | b'^' | b'_' | b'.' | b'+' | b'-'
                    )
            })
    })
}

fn message_for(code: ErrorCode) -> &'static str {
    match code {
        ErrorCode::MalformedEnvelope => "analyzer request is malformed",
        ErrorCode::RequestTooLarge => "analyzer request exceeds the envelope limit",
        ErrorCode::UnsupportedProtocol => "analyzer protocol is unsupported",
        ErrorCode::InvalidRequest => "analyzer request violates the protocol contract",
        ErrorCode::CapabilityDenied => "requested analyzer capability is disabled",
        ErrorCode::UnsupportedAnalyzer => "analyzer is unsupported",
        ErrorCode::InputLimitExceeded => "analyzer input exceeds its declared limit",
        ErrorCode::InvalidContent => "analyzer input content is invalid",
        ErrorCode::OutputLimitExceeded => "analyzer output exceeds its declared limit",
        ErrorCode::ResultTooLarge => "analyzer result exceeds the envelope limit",
        ErrorCode::DeadlineExceeded => "analyzer deadline was exceeded",
        ErrorCode::ProcessFailed => "analyzer process failed",
        ErrorCode::InvalidResult => "analyzer result violates the protocol contract",
        ErrorCode::InternalError => "analyzer failed internally",
    }
}

fn retryable(code: ErrorCode) -> bool {
    matches!(code, ErrorCode::DeadlineExceeded | ErrorCode::ProcessFailed)
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RequestWire {
    protocol_version: Option<String>,
    request_id: Option<String>,
    analyzer: Option<String>,
    input: Option<InputWire>,
    limits: Option<LimitsWire>,
    capabilities: Option<CapabilitiesWire>,
    metadata_only: Option<bool>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct InputWire {
    media_type: Option<String>,
    content_base64: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct LimitsWire {
    max_input_bytes: Option<i64>,
    max_output_bytes: Option<i64>,
    timeout_ms: Option<i64>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CapabilitiesWire {
    filesystem: Option<bool>,
    network: Option<bool>,
    subprocess: Option<bool>,
    environment: Option<bool>,
}

impl RequestWire {
    fn into_request(self) -> Result<Request, ErrorCode> {
        let input = self.input.ok_or(ErrorCode::InvalidRequest)?;
        let limits = self.limits.ok_or(ErrorCode::InvalidRequest)?;
        let capabilities = self.capabilities.ok_or(ErrorCode::InvalidRequest)?;
        Ok(Request {
            protocol_version: self.protocol_version.ok_or(ErrorCode::InvalidRequest)?,
            request_id: self.request_id.ok_or(ErrorCode::InvalidRequest)?,
            analyzer: self.analyzer.ok_or(ErrorCode::InvalidRequest)?,
            input: Input {
                media_type: input.media_type.ok_or(ErrorCode::InvalidRequest)?,
                content_base64: input.content_base64.ok_or(ErrorCode::InvalidRequest)?,
            },
            limits: Limits {
                max_input_bytes: limits.max_input_bytes.ok_or(ErrorCode::InvalidRequest)?,
                max_output_bytes: limits.max_output_bytes.ok_or(ErrorCode::InvalidRequest)?,
                timeout_ms: limits.timeout_ms.ok_or(ErrorCode::InvalidRequest)?,
            },
            capabilities: Capabilities {
                filesystem: capabilities.filesystem.ok_or(ErrorCode::InvalidRequest)?,
                network: capabilities.network.ok_or(ErrorCode::InvalidRequest)?,
                subprocess: capabilities.subprocess.ok_or(ErrorCode::InvalidRequest)?,
                environment: capabilities.environment.ok_or(ErrorCode::InvalidRequest)?,
            },
            metadata_only: self.metadata_only.ok_or(ErrorCode::InvalidRequest)?,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request() -> Request {
        Request {
            protocol_version: REQUEST_PROTOCOL_VERSION.to_owned(),
            request_id: "request-1".to_owned(),
            analyzer: FIXTURE_ANALYZER_NAME.to_owned(),
            input: Input {
                media_type: "application/octet-stream".to_owned(),
                content_base64: String::new(),
            },
            limits: Limits {
                max_input_bytes: MAX_DECODED_INPUT_BYTES as i64,
                max_output_bytes: 4096,
                timeout_ms: 5000,
            },
            capabilities: disabled_capabilities(),
            metadata_only: true,
        }
    }

    #[test]
    fn evaluates_metadata_without_returning_content() {
        let mut value = request();
        value.input.media_type = "text/plain".to_owned();
        value.input.content_base64 = STANDARD.encode(b"alpha\nbeta\n");
        let evaluation = evaluate(&serde_json::to_vec(&value).unwrap());
        assert_eq!(evaluation.exit_code, EXIT_SUCCESS);
        let result: ResultEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(result.summary.input_bytes, 11);
        assert_eq!(result.summary.line_count, 2);
        assert!(result.summary.utf8);
        assert_eq!(
            result.summary.sha256,
            "e49c81e2d2f84e259d40e2fb8192f3bcd198b355184845d76d8f58807d0d78ee"
        );
        assert!(!evaluation.stdout.windows(5).any(|part| part == b"alpha"));
        assert!(!capabilities_enabled(&result.capabilities_used));
    }

    #[test]
    fn rejects_unknown_duplicate_and_enabled_capability() {
        let valid = String::from_utf8(serde_json::to_vec(&request()).unwrap()).unwrap();
        for malformed in [
            valid.replace(
                "\"metadata_only\":true",
                "\"metadata_only\":true,\"extra\":false",
            ),
            valid.replace("\"network\":false", "\"network\":false,\"network\":false"),
        ] {
            let evaluation = evaluate(malformed.as_bytes());
            let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
            assert_eq!(error.code, ErrorCode::MalformedEnvelope);
            assert!(error.request_id.is_empty());
        }
        let deep = format!("{}{}", "[".repeat(256), "]".repeat(256));
        let evaluation = evaluate(deep.as_bytes());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::MalformedEnvelope);
        assert!(error.request_id.is_empty());

        let enabled = valid.replace("\"network\":false", "\"network\":true");
        let evaluation = evaluate(enabled.as_bytes());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::CapabilityDenied);
        assert_eq!(error.request_id, "request-1");

        let missing = valid.replace(",\"environment\":false", "");
        let evaluation = evaluate(missing.as_bytes());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::InvalidRequest);
        assert!(error.request_id.is_empty());
    }

    #[test]
    fn enforces_declared_input_and_output_limits() {
        let mut input_limited = request();
        input_limited.limits.max_input_bytes = 4;
        input_limited.input.content_base64 = STANDARD.encode(b"12345");
        let evaluation = evaluate(&serde_json::to_vec(&input_limited).unwrap());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::InputLimitExceeded);

        let mut output_limited = request();
        output_limited.request_id = "r".repeat(MAX_REQUEST_ID_BYTES);
        output_limited.input.media_type = format!("application/{}", "x".repeat(116));
        output_limited.limits.max_output_bytes = MIN_RESULT_ENVELOPE_BYTES as i64;
        let evaluation = evaluate(&serde_json::to_vec(&output_limited).unwrap());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::OutputLimitExceeded);
    }

    #[test]
    fn bounds_stdin_before_parsing() {
        let raw = vec![b' '; MAX_REQUEST_ENVELOPE_BYTES + 20];
        let mut stdout = Vec::new();
        let exit = run_io(raw.as_slice(), &mut stdout).unwrap();
        let error: ErrorEnvelope = serde_json::from_slice(&stdout).unwrap();
        assert_eq!(exit, EXIT_REJECTED);
        assert_eq!(error.code, ErrorCode::RequestTooLarge);
    }

    #[test]
    fn does_not_reflect_an_invalid_request_id() {
        let mut value = request();
        value.request_id = "private value".to_owned();
        let evaluation = evaluate(&serde_json::to_vec(&value).unwrap());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::InvalidRequest);
        assert!(error.request_id.is_empty());
    }

    #[test]
    fn rejects_wrong_archive_media_type_and_malformed_zip() {
        let mut value = request();
        value.analyzer = ARCHIVE_ANALYZER_NAME.to_owned();
        value.input.media_type = "text/plain".to_owned();
        value.input.content_base64 = STANDARD.encode(b"not a zip");
        let evaluation = evaluate(&serde_json::to_vec(&value).unwrap());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::InvalidRequest);

        value.input.media_type = "application/zip".to_owned();
        let evaluation = evaluate(&serde_json::to_vec(&value).unwrap());
        let error: ErrorEnvelope = serde_json::from_slice(&evaluation.stdout).unwrap();
        assert_eq!(error.code, ErrorCode::InvalidContent);
    }

    #[test]
    fn saturates_hostile_archive_size_metadata() {
        let inventory = build_archive_inventory(
            "archive-overflow",
            vec![
                ArchiveEntry {
                    index: 0,
                    name: "first.bin".to_owned(),
                    kind: String::new(),
                    compressed_bytes: 1,
                    uncompressed_bytes: u64::MAX,
                    compression_ratio_milli: 0,
                    declared_crc32: "00000000".to_owned(),
                    risk_codes: Vec::new(),
                },
                ArchiveEntry {
                    index: 1,
                    name: "second.bin".to_owned(),
                    kind: String::new(),
                    compressed_bytes: u64::MAX,
                    uncompressed_bytes: 1,
                    compression_ratio_milli: 0,
                    declared_crc32: "00000000".to_owned(),
                    risk_codes: Vec::new(),
                },
            ],
        )
        .unwrap();
        assert_eq!(inventory.total_compressed_bytes, u64::MAX);
        assert_eq!(inventory.total_uncompressed_bytes, u64::MAX);
        assert_eq!(
            archive_ratio_milli(u64::MAX, 1),
            MAX_ARCHIVE_REPORTED_RATIO_MILLI
        );
        assert!(
            inventory
                .risk_codes
                .contains(&ArchiveRiskCode::DeclaredTotalSize)
        );
    }
}
