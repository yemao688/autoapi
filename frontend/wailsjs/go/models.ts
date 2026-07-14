export namespace api {
	
	export class ExportResult {
	    filename: string;
	    data: number[];
	
	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.data = source["data"];
	    }
	}
	export class ProxyStatus {
	    running: boolean;
	    url?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.url = source["url"];
	    }
	}
	export class RuntimePaths {
	    storage_dir: string;
	    log_path: string;
	
	    static createFrom(source: any = {}) {
	        return new RuntimePaths(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.storage_dir = source["storage_dir"];
	        this.log_path = source["log_path"];
	    }
	}

}

export namespace model {
	
	export class AdvancedSettings {
	    debug_mode: boolean;
	    experimental: boolean;
	    http_proxy: string;
	    feature_capability_enforcement: string;
	
	    static createFrom(source: any = {}) {
	        return new AdvancedSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.debug_mode = source["debug_mode"];
	        this.experimental = source["experimental"];
	        this.http_proxy = source["http_proxy"];
	        this.feature_capability_enforcement = source["feature_capability_enforcement"];
	    }
	}
	export class ApiKey {
	    id: string;
	    name: string;
	    expires_at: number;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ApiKey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.expires_at = source["expires_at"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ApiKeyInput {
	    name: string;
	    expires_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ApiKeyInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class AppInfo {
	    version: string;
	    build: string;
	    platform: string;
	    arch: string;
	    goVersion: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.build = source["build"];
	        this.platform = source["platform"];
	        this.arch = source["arch"];
	        this.goVersion = source["goVersion"];
	    }
	}
	export class AppearanceSettings {
	    theme: string;
	    density: string;
	    accent_color: string;
	
	    static createFrom(source: any = {}) {
	        return new AppearanceSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.density = source["density"];
	        this.accent_color = source["accent_color"];
	    }
	}
	export class ServiceHealth {
	    status: string;
	    uptime_seconds: number;
	    cpu_percent: number;
	    memory_mb: number;
	    active_connections: number;
	    websocket_count: number;
	    http_count: number;
	    proxy_url: string;
	    api_address: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceHealth(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.uptime_seconds = source["uptime_seconds"];
	        this.cpu_percent = source["cpu_percent"];
	        this.memory_mb = source["memory_mb"];
	        this.active_connections = source["active_connections"];
	        this.websocket_count = source["websocket_count"];
	        this.http_count = source["http_count"];
	        this.proxy_url = source["proxy_url"];
	        this.api_address = source["api_address"];
	    }
	}
	export class RequestLogChainEntry {
	    attempt_order: number;
	    provider_id: string;
	    provider_name: string;
	    model_name: string;
	    target_id: string;
	    status: string;
	    status_code: number;
	    error: string;
	    latency_ms: number;
	    first_token_ms: number;
	    upstream_started: boolean;
	    request_cost: number;
	    request_cost_available: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RequestLogChainEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.attempt_order = source["attempt_order"];
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.model_name = source["model_name"];
	        this.target_id = source["target_id"];
	        this.status = source["status"];
	        this.status_code = source["status_code"];
	        this.error = source["error"];
	        this.latency_ms = source["latency_ms"];
	        this.first_token_ms = source["first_token_ms"];
	        this.upstream_started = source["upstream_started"];
	        this.request_cost = source["request_cost"];
	        this.request_cost_available = source["request_cost_available"];
	    }
	}
	export class RequestLog {
	    id: string;
	    timestamp: number;
	    status_code: number;
	    provider_id: string;
	    provider_name: string;
	    model: string;
	    input_tokens: number;
	    output_tokens: number;
	    cache_creation: number;
	    cache_hit: number;
	    cost: number;
	    cost_available: boolean;
	    latency_ms: number;
	    first_token_ms: number;
	    is_stream: boolean;
	    route_id: string;
	    route_label: string;
	    api_key_id: string;
	    error?: string;
	    user_agent: string;
	    client_ip: string;
	    request_id: string;
	    request_uri: string;
	    chain: RequestLogChainEntry[];
	
	    static createFrom(source: any = {}) {
	        return new RequestLog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.timestamp = source["timestamp"];
	        this.status_code = source["status_code"];
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.model = source["model"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cache_creation = source["cache_creation"];
	        this.cache_hit = source["cache_hit"];
	        this.cost = source["cost"];
	        this.cost_available = source["cost_available"];
	        this.latency_ms = source["latency_ms"];
	        this.first_token_ms = source["first_token_ms"];
	        this.is_stream = source["is_stream"];
	        this.route_id = source["route_id"];
	        this.route_label = source["route_label"];
	        this.api_key_id = source["api_key_id"];
	        this.error = source["error"];
	        this.user_agent = source["user_agent"];
	        this.client_ip = source["client_ip"];
	        this.request_id = source["request_id"];
	        this.request_uri = source["request_uri"];
	        this.chain = this.convertValues(source["chain"], RequestLogChainEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ModelRuleSummary {
	    id: string;
	    name: string;
	    enabled: boolean;
	    today_success_rate?: number;
	    today_request_count: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelRuleSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.enabled = source["enabled"];
	        this.today_success_rate = source["today_success_rate"];
	        this.today_request_count = source["today_request_count"];
	    }
	}
	export class Provider {
	    id: string;
	    name: string;
	    base_url: string;
	    status: string;
	    key_masked: string;
	    models_count: number;
	    monthly_tokens: number;
	    avg_latency_ms: number;
	    last_tested_at: number;
	    error_message?: string;
	    is_custom: boolean;
	    responses_enabled: boolean;
	    messages_enabled: boolean;
	    gemini_enabled: boolean;
	    enabled: boolean;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new Provider(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.base_url = source["base_url"];
	        this.status = source["status"];
	        this.key_masked = source["key_masked"];
	        this.models_count = source["models_count"];
	        this.monthly_tokens = source["monthly_tokens"];
	        this.avg_latency_ms = source["avg_latency_ms"];
	        this.last_tested_at = source["last_tested_at"];
	        this.error_message = source["error_message"];
	        this.is_custom = source["is_custom"];
	        this.responses_enabled = source["responses_enabled"];
	        this.messages_enabled = source["messages_enabled"];
	        this.gemini_enabled = source["gemini_enabled"];
	        this.enabled = source["enabled"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class TokenTrendPoint {
	    date: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost: number;
	
	    static createFrom(source: any = {}) {
	        return new TokenTrendPoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost = source["cost"];
	    }
	}
	export class Stat {
	    label: string;
	    value: string;
	    delta: string;
	    trend: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new Stat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.value = source["value"];
	        this.delta = source["delta"];
	        this.trend = source["trend"];
	        this.note = source["note"];
	    }
	}
	export class DashboardData {
	    stats: Stat[];
	    token_trend: TokenTrendPoint[];
	    providers: Provider[];
	    model_rules: ModelRuleSummary[];
	    recent_activity: RequestLog[];
	    service_health: ServiceHealth;
	
	    static createFrom(source: any = {}) {
	        return new DashboardData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stats = this.convertValues(source["stats"], Stat);
	        this.token_trend = this.convertValues(source["token_trend"], TokenTrendPoint);
	        this.providers = this.convertValues(source["providers"], Provider);
	        this.model_rules = this.convertValues(source["model_rules"], ModelRuleSummary);
	        this.recent_activity = this.convertValues(source["recent_activity"], RequestLog);
	        this.service_health = this.convertValues(source["service_health"], ServiceHealth);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DataSettings {
	    log_retention_days: number;
	    storage_path: string;
	
	    static createFrom(source: any = {}) {
	        return new DataSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.log_retention_days = source["log_retention_days"];
	        this.storage_path = source["storage_path"];
	    }
	}
	export class EffectiveCost {
	    cost: number;
	    currency: string;
	    available: boolean;
	
	    static createFrom(source: any = {}) {
	        return new EffectiveCost(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cost = source["cost"];
	        this.currency = source["currency"];
	        this.available = source["available"];
	    }
	}
	export class Endpoint {
	    method: string;
	    path: string;
	    desc: string;
	
	    static createFrom(source: any = {}) {
	        return new Endpoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.method = source["method"];
	        this.path = source["path"];
	        this.desc = source["desc"];
	    }
	}
	export class GeneralSettings {
	    launch_at_login: boolean;
	    startup_action: string;
	    menu_bar_item: boolean;
	    close_action: string;
	
	    static createFrom(source: any = {}) {
	        return new GeneralSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.launch_at_login = source["launch_at_login"];
	        this.startup_action = source["startup_action"];
	        this.menu_bar_item = source["menu_bar_item"];
	        this.close_action = source["close_action"];
	    }
	}
	export class LogQuery {
	    start_date: number;
	    end_date: number;
	    provider: string;
	    route_id: string;
	    model: string;
	    status: string;
	    search: string;
	    page: number;
	    page_size: number;
	
	    static createFrom(source: any = {}) {
	        return new LogQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start_date = source["start_date"];
	        this.end_date = source["end_date"];
	        this.provider = source["provider"];
	        this.route_id = source["route_id"];
	        this.model = source["model"];
	        this.status = source["status"];
	        this.search = source["search"];
	        this.page = source["page"];
	        this.page_size = source["page_size"];
	    }
	}
	export class LogQueryResult {
	    logs: RequestLog[];
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new LogQueryResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.logs = this.convertValues(source["logs"], RequestLog);
	        this.total = source["total"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LoggingSettings {
	    enabled: boolean;
	    level: string;
	    max_size_mb: number;
	    max_age_days: number;
	    max_backups: number;
	
	    static createFrom(source: any = {}) {
	        return new LoggingSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.level = source["level"];
	        this.max_size_mb = source["max_size_mb"];
	        this.max_age_days = source["max_age_days"];
	        this.max_backups = source["max_backups"];
	    }
	}
	export class Model {
	    id: string;
	    provider_id: string;
	    name: string;
	    context_window: number;
	    owned_by: string;
	    active: boolean;
	    request_price: number;
	    latency_ms: number;
	    updated_at: number;
	    created_at: number;
	
	    static createFrom(source: any = {}) {
	        return new Model(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.provider_id = source["provider_id"];
	        this.name = source["name"];
	        this.context_window = source["context_window"];
	        this.owned_by = source["owned_by"];
	        this.active = source["active"];
	        this.request_price = source["request_price"];
	        this.latency_ms = source["latency_ms"];
	        this.updated_at = source["updated_at"];
	        this.created_at = source["created_at"];
	    }
	}
	export class ModelCapability {
	    provider_id: string;
	    model_name: string;
	    protocol: string;
	    feature: string;
	    enabled: boolean;
	    source: string;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelCapability(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider_id = source["provider_id"];
	        this.model_name = source["model_name"];
	        this.protocol = source["protocol"];
	        this.feature = source["feature"];
	        this.enabled = source["enabled"];
	        this.source = source["source"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ModelChatTestResult {
	    ok: boolean;
	    response: string;
	    latency_ms: number;
	    finish_reason?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelChatTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.response = source["response"];
	        this.latency_ms = source["latency_ms"];
	        this.finish_reason = source["finish_reason"];
	        this.error = source["error"];
	    }
	}
	export class ModelRanking {
	    model: string;
	    provider_name: string;
	    requests: number;
	    tokens: number;
	    cost: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelRanking(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.provider_name = source["provider_name"];
	        this.requests = source["requests"];
	        this.tokens = source["tokens"];
	        this.cost = source["cost"];
	    }
	}
	export class ModelRuleTarget {
	    id: string;
	    rule_id: string;
	    provider_id: string;
	    model_name: string;
	    max_retries: number;
	    tier: number;
	    first_token_timeout_seconds: number;
	    hit_count: number;
	    failure_count: number;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelRuleTarget(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.rule_id = source["rule_id"];
	        this.provider_id = source["provider_id"];
	        this.model_name = source["model_name"];
	        this.max_retries = source["max_retries"];
	        this.tier = source["tier"];
	        this.first_token_timeout_seconds = source["first_token_timeout_seconds"];
	        this.hit_count = source["hit_count"];
	        this.failure_count = source["failure_count"];
	        this.enabled = source["enabled"];
	    }
	}
	export class ModelRule {
	    id: string;
	    name: string;
	    enabled: boolean;
	    first_byte_timeout_seconds: number;
	    strategy: string;
	    created_at: number;
	    updated_at: number;
	    targets: ModelRuleTarget[];
	    today_success_rate?: number;
	    today_request_count: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelRule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.enabled = source["enabled"];
	        this.first_byte_timeout_seconds = source["first_byte_timeout_seconds"];
	        this.strategy = source["strategy"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.targets = this.convertValues(source["targets"], ModelRuleTarget);
	        this.today_success_rate = source["today_success_rate"];
	        this.today_request_count = source["today_request_count"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ModelRuleTargetInput {
	    id: string;
	    provider_id: string;
	    model_name: string;
	    max_retries: number;
	    tier?: number;
	    first_token_timeout_seconds: number;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelRuleTargetInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.provider_id = source["provider_id"];
	        this.model_name = source["model_name"];
	        this.max_retries = source["max_retries"];
	        this.tier = source["tier"];
	        this.first_token_timeout_seconds = source["first_token_timeout_seconds"];
	        this.enabled = source["enabled"];
	    }
	}
	export class ModelRuleInput {
	    name: string;
	    enabled: boolean;
	    first_byte_timeout_seconds: number;
	    strategy: string;
	    targets: ModelRuleTargetInput[];
	
	    static createFrom(source: any = {}) {
	        return new ModelRuleInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.enabled = source["enabled"];
	        this.first_byte_timeout_seconds = source["first_byte_timeout_seconds"];
	        this.strategy = source["strategy"];
	        this.targets = this.convertValues(source["targets"], ModelRuleTargetInput);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ShadowPlanCandidate {
	    target_id: string;
	    tier: number;
	    available: boolean;
	    reason: string;
	    changed: boolean;
	    circuit_state?: string;
	
	    static createFrom(source: any = {}) {
	        return new ShadowPlanCandidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target_id = source["target_id"];
	        this.tier = source["tier"];
	        this.available = source["available"];
	        this.reason = source["reason"];
	        this.changed = source["changed"];
	        this.circuit_state = source["circuit_state"];
	    }
	}
	export class ModelRuleShadowComparison {
	    rule_id: string;
	    rule_name: string;
	    strategy: string;
	    original_order: string[];
	    planned_order: string[];
	    changed: boolean;
	    candidates: ShadowPlanCandidate[];
	    rejected: ShadowPlanCandidate[];
	    assumptions: string[];
	
	    static createFrom(source: any = {}) {
	        return new ModelRuleShadowComparison(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rule_id = source["rule_id"];
	        this.rule_name = source["rule_name"];
	        this.strategy = source["strategy"];
	        this.original_order = source["original_order"];
	        this.planned_order = source["planned_order"];
	        this.changed = source["changed"];
	        this.candidates = this.convertValues(source["candidates"], ShadowPlanCandidate);
	        this.rejected = this.convertValues(source["rejected"], ShadowPlanCandidate);
	        this.assumptions = source["assumptions"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class ModelTestResult {
	    ok: boolean;
	    latency_ms: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.latency_ms = source["latency_ms"];
	        this.error = source["error"];
	    }
	}
	
	export class ProviderCapability {
	    provider_id: string;
	    protocol: string;
	    feature: string;
	    enabled: boolean;
	    source: string;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ProviderCapability(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider_id = source["provider_id"];
	        this.protocol = source["protocol"];
	        this.feature = source["feature"];
	        this.enabled = source["enabled"];
	        this.source = source["source"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ProviderInput {
	    name: string;
	    base_url: string;
	    upstream_key: string;
	    is_custom: boolean;
	    responses_enabled: boolean;
	    messages_enabled: boolean;
	    gemini_enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ProviderInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.base_url = source["base_url"];
	        this.upstream_key = source["upstream_key"];
	        this.is_custom = source["is_custom"];
	        this.responses_enabled = source["responses_enabled"];
	        this.messages_enabled = source["messages_enabled"];
	        this.gemini_enabled = source["gemini_enabled"];
	    }
	}
	export class ProviderModelUpdate {
	    provider_id: string;
	    old_name: string;
	    name: string;
	    request_price: number;
	
	    static createFrom(source: any = {}) {
	        return new ProviderModelUpdate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider_id = source["provider_id"];
	        this.old_name = source["old_name"];
	        this.name = source["name"];
	        this.request_price = source["request_price"];
	    }
	}
	export class ProviderShare {
	    provider_id: string;
	    provider_name: string;
	    tokens: number;
	    cost: number;
	    percent: number;
	
	    static createFrom(source: any = {}) {
	        return new ProviderShare(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.tokens = source["tokens"];
	        this.cost = source["cost"];
	        this.percent = source["percent"];
	    }
	}
	export class ProviderTestResult {
	    ok: boolean;
	    latency_ms: number;
	    error?: string;
	    models: string[];
	
	    static createFrom(source: any = {}) {
	        return new ProviderTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.latency_ms = source["latency_ms"];
	        this.error = source["error"];
	        this.models = source["models"];
	    }
	}
	export class ReorderModelRuleTargetsResult {
	    conflict: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ReorderModelRuleTargetsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conflict = source["conflict"];
	    }
	}
	export class ReorderModelRulesResult {
	    conflict: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ReorderModelRulesResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conflict = source["conflict"];
	    }
	}
	export class TargetMetricKey {
	    target_id?: string;
	    provider_id: string;
	    model_name: string;
	    endpoint: string;
	
	    static createFrom(source: any = {}) {
	        return new TargetMetricKey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target_id = source["target_id"];
	        this.provider_id = source["provider_id"];
	        this.model_name = source["model_name"];
	        this.endpoint = source["endpoint"];
	    }
	}
	export class TargetRuntimeSummary {
	    key: TargetMetricKey;
	    requests: number;
	    attempts: number;
	    successes: number;
	    failures: number;
	    status_429: number;
	    status_5xx: number;
	    transport: number;
	    client_aborts: number;
	    truncated: number;
	    downstream: number;
	    // Go type: time
	    last_used: any;
	    // Go type: time
	    last_success: any;
	    // Go type: time
	    last_failure: any;
	    // Go type: time
	    updated_at: any;
	
	    static createFrom(source: any = {}) {
	        return new TargetRuntimeSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = this.convertValues(source["key"], TargetMetricKey);
	        this.requests = source["requests"];
	        this.attempts = source["attempts"];
	        this.successes = source["successes"];
	        this.failures = source["failures"];
	        this.status_429 = source["status_429"];
	        this.status_5xx = source["status_5xx"];
	        this.transport = source["transport"];
	        this.client_aborts = source["client_aborts"];
	        this.truncated = source["truncated"];
	        this.downstream = source["downstream"];
	        this.last_used = this.convertValues(source["last_used"], null);
	        this.last_success = this.convertValues(source["last_success"], null);
	        this.last_failure = this.convertValues(source["last_failure"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TargetShadowScore {
	    target_id: string;
	    rule_id: string;
	    rule_name: string;
	    provider_id: string;
	    provider_name: string;
	    model_name: string;
	    tier: number;
	    metrics: TargetRuntimeSummary;
	    metrics_fresh: boolean;
	    endpoint: string;
	    endpoint_assumed: boolean;
	    reliability: number;
	    latency: number;
	    ttft: number;
	    capacity: number;
	    cost_efficiency: number;
	    confidence: number;
	    sample_count: number;
	    overall: number;
	    exploration_bonus: number;
	    estimated_cost: number;
	    cost: EffectiveCost;
	    availability: string;
	    reason: string;
	    circuit_state?: string;
	
	    static createFrom(source: any = {}) {
	        return new TargetShadowScore(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target_id = source["target_id"];
	        this.rule_id = source["rule_id"];
	        this.rule_name = source["rule_name"];
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.model_name = source["model_name"];
	        this.tier = source["tier"];
	        this.metrics = this.convertValues(source["metrics"], TargetRuntimeSummary);
	        this.metrics_fresh = source["metrics_fresh"];
	        this.endpoint = source["endpoint"];
	        this.endpoint_assumed = source["endpoint_assumed"];
	        this.reliability = source["reliability"];
	        this.latency = source["latency"];
	        this.ttft = source["ttft"];
	        this.capacity = source["capacity"];
	        this.cost_efficiency = source["cost_efficiency"];
	        this.confidence = source["confidence"];
	        this.sample_count = source["sample_count"];
	        this.overall = source["overall"];
	        this.exploration_bonus = source["exploration_bonus"];
	        this.estimated_cost = source["estimated_cost"];
	        this.cost = this.convertValues(source["cost"], EffectiveCost);
	        this.availability = source["availability"];
	        this.reason = source["reason"];
	        this.circuit_state = source["circuit_state"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ReplayAttemptScore {
	    attempt: RequestLogChainEntry;
	    target_id: string;
	    provider_id: string;
	    model_name: string;
	    target_missing: boolean;
	    provider_missing: boolean;
	    score: TargetShadowScore;
	    replay_limitation?: string;
	
	    static createFrom(source: any = {}) {
	        return new ReplayAttemptScore(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.attempt = this.convertValues(source["attempt"], RequestLogChainEntry);
	        this.target_id = source["target_id"];
	        this.provider_id = source["provider_id"];
	        this.model_name = source["model_name"];
	        this.target_missing = source["target_missing"];
	        this.provider_missing = source["provider_missing"];
	        this.score = this.convertValues(source["score"], TargetShadowScore);
	        this.replay_limitation = source["replay_limitation"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ReplayResult {
	    log_id: string;
	    timestamp: number;
	    rule_id: string;
	    rule_name: string;
	    request_outcome: string;
	    selected_target: string;
	    endpoint: string;
	    endpoint_assumed: boolean;
	    attempts: ReplayAttemptScore[];
	    warnings?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ReplayResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.log_id = source["log_id"];
	        this.timestamp = source["timestamp"];
	        this.rule_id = source["rule_id"];
	        this.rule_name = source["rule_name"];
	        this.request_outcome = source["request_outcome"];
	        this.selected_target = source["selected_target"];
	        this.endpoint = source["endpoint"];
	        this.endpoint_assumed = source["endpoint_assumed"];
	        this.attempts = this.convertValues(source["attempts"], ReplayAttemptScore);
	        this.warnings = source["warnings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class RoutingSettings {
	    streaming_sse: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RoutingSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.streaming_sse = source["streaming_sse"];
	    }
	}
	export class ServerSettings {
	    port: number;
	    bind_address: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.port = source["port"];
	        this.bind_address = source["bind_address"];
	    }
	}
	
	export class Settings {
	    general: GeneralSettings;
	    appearance: AppearanceSettings;
	    routing: RoutingSettings;
	    server: ServerSettings;
	    data: DataSettings;
	    advanced: AdvancedSettings;
	    logging: LoggingSettings;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.general = this.convertValues(source["general"], GeneralSettings);
	        this.appearance = this.convertValues(source["appearance"], AppearanceSettings);
	        this.routing = this.convertValues(source["routing"], RoutingSettings);
	        this.server = this.convertValues(source["server"], ServerSettings);
	        this.data = this.convertValues(source["data"], DataSettings);
	        this.advanced = this.convertValues(source["advanced"], AdvancedSettings);
	        this.logging = this.convertValues(source["logging"], LoggingSettings);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	
	
	export class UsageStats {
	    token_stats: Stat[];
	    providers: ProviderShare[];
	    model_ranking: ModelRanking[];
	    log_stats: Stat[];
	
	    static createFrom(source: any = {}) {
	        return new UsageStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token_stats = this.convertValues(source["token_stats"], Stat);
	        this.providers = this.convertValues(source["providers"], ProviderShare);
	        this.model_ranking = this.convertValues(source["model_ranking"], ModelRanking);
	        this.log_stats = this.convertValues(source["log_stats"], Stat);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UsageTrendBucket {
	    bucket: string;
	    cost: number;
	    cache_creation: number;
	    cache_hit: number;
	    input: number;
	    output: number;
	
	    static createFrom(source: any = {}) {
	        return new UsageTrendBucket(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bucket = source["bucket"];
	        this.cost = source["cost"];
	        this.cache_creation = source["cache_creation"];
	        this.cache_hit = source["cache_hit"];
	        this.input = source["input"];
	        this.output = source["output"];
	    }
	}
	export class UsageTrends {
	    range: string;
	    bucket_size: string;
	    buckets: UsageTrendBucket[];
	
	    static createFrom(source: any = {}) {
	        return new UsageTrends(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.range = source["range"];
	        this.bucket_size = source["bucket_size"];
	        this.buckets = this.convertValues(source["buckets"], UsageTrendBucket);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UsageTrendsQuery {
	    start_date: number;
	    end_date: number;
	    provider: string;
	    route_id: string;
	    model: string;
	    search: string;
	
	    static createFrom(source: any = {}) {
	        return new UsageTrendsQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start_date = source["start_date"];
	        this.end_date = source["end_date"];
	        this.provider = source["provider"];
	        this.route_id = source["route_id"];
	        this.model = source["model"];
	        this.search = source["search"];
	    }
	}

}

